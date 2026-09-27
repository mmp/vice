// client/stt_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// The whisper prompt carries each name of the reporting points ahead of the
// aircraft on the user's frequency, once however many of them are expecting
// the approach.
func TestWhisperPromptReportingPoints(t *testing.T) {
	oldDB := db.DB
	db.DB = &db.StaticDatabase{Callsigns: map[string]string{"AAL": "American"}}
	t.Cleanup(func() { db.DB = oldDB })

	bridge := av.ReportingPoint{Names: []string{"Dumbarton bridge", "Dumbarton"}}
	tower := av.ReportingPoint{Names: []string{"Sutro tower"}}
	track := func(callsign av.ADSBCallsign, freq sim.ControlPosition, rp av.ReportingPoint) *sim.Track {
		return &sim.Track{
			RadarTrack:          av.RadarTrack{ADSBCallsign: callsign},
			ControllerFrequency: freq,
			ReportingPoints:     []av.ReportingPoint{rp},
		}
	}

	var ss SimState
	ss.UserTCW = "TEST"
	ss.CurrentConsolidation = map[sim.TCW]*sim.TCPConsolidation{"TEST": {PrimaryTCP: "TEST"}}
	ss.Tracks = map[av.ADSBCallsign]*sim.Track{
		"AAL1": track("AAL1", "TEST", bridge),
		"AAL2": track("AAL2", "TEST", bridge),
		"AAL3": track("AAL3", "OTHER", tower),
	}

	parts := strings.Split(makeWhisperPrompt(ss), ", ")
	count := func(s string) int {
		return len(util.FilterSlice(parts, func(p string) bool { return p == s }))
	}
	for _, name := range bridge.Names {
		if n := count(name); n != 1 {
			t.Errorf("%q appears %d times in the prompt, want once", name, n)
		}
	}
	if count("Sutro tower") != 0 {
		t.Error("prompt includes the reporting point of an aircraft on another frequency")
	}
}

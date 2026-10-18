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
		fp      sim.FlightPlan
		alt     float32
		reached bool
		want    string
	}{
		{"at assigned", sim.FlightPlan{AssignedAltitude: 30000}, 30000, true, "300C"},
		{"climbing", sim.FlightPlan{AssignedAltitude: 30000}, 25300, false, "300" + upArrow + "253"},
		{"descending", sim.FlightPlan{AssignedAltitude: 23000}, 25300, false, "230" + downArrow + "253"},
		{"climbed through", sim.FlightPlan{AssignedAltitude: 23000}, 25300, true, "230+253"},
		{"descended below", sim.FlightPlan{AssignedAltitude: 23000}, 21200, true, "230-212"},
		{"interim", sim.FlightPlan{AssignedAltitude: 30000, InterimAlt: 10000}, 25300, false, "100T253"},
		{"above block", sim.FlightPlan{AltitudeBlock: [2]int{20000, 25000}}, 35300, false, "200B353"},
		{"within block", sim.FlightPlan{AltitudeBlock: [2]int{20000, 25000}}, 22100, false, "200B250"},
		{"block ceiling", sim.FlightPlan{AltitudeBlock: [2]int{20000, 25000}}, 25040, false, "200B250"},
		{"below block", sim.FlightPlan{AltitudeBlock: [2]int{20000, 25000}}, 19300, false, "200B193"},
		{"VFR", sim.FlightPlan{AssignedAltitudeQualifier: sim.VFRAltitude}, 5500, false, "VFR/055"},
		{"VFR at an altitude", sim.FlightPlan{AssignedAltitude: 10500, AssignedAltitudeQualifier: sim.VFRAltitude},
			5500, false, "VFR/055"},
		{"VFR-on-top", sim.FlightPlan{AssignedAltitudeQualifier: sim.OTPAltitude}, 21800, false, "OTP/218"},
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

// An unpaired LDB shows the beacon code; with the BCAST FLID filter off it
// shows the ADS-B callsign instead, unless the code is 1200.
func TestUnpairedLDBIdentifier(t *testing.T) {
	ep := &Scope{prefSet: &PrefrenceSet{Current: *makeDefaultPreferences()}}
	for _, c := range []struct {
		name      string
		squawk    av.Squawk
		callsigns bool
		want      string
	}{
		{"filter active", 0o4721, false, "4721"},
		{"filter active, 1200", 0o1200, false, "1200"},
		{"filter inactive", 0o4721, true, "N810FR"},
		{"filter inactive, 1200", 0o1200, true, "1200"},
	} {
		ep.currentPrefs().UnpairedLDBCallsigns = c.callsigns
		trk := sim.Track{RadarTrack: av.RadarTrack{ADSBCallsign: "N810FR"}}
		state := &TrackState{Track: av.RadarTrack{ADSBCallsign: "N810FR", Squawk: c.squawk}}
		if got := ep.limitedDatablockID(trk, state); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}

	paired := sim.Track{RadarTrack: av.RadarTrack{ADSBCallsign: "N810FR"}, FlightPlan: &sim.FlightPlan{ACID: "LIFEGUARD1"}}
	if got := ep.limitedDatablockID(paired, &TrackState{}); got != "LIFEGUARD1" {
		t.Errorf("paired: got %q, want LIFEGUARD1", got)
	}
}

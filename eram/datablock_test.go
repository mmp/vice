// eram/datablock_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// dbText returns the characters of a datablock field, with unset ones as
// spaces and trailing ones removed.
func dbText(f []dbChar) string {
	var sb strings.Builder
	for _, ch := range dbChopTrailing(f) {
		sb.WriteRune(util.Select(ch.ch == 0, ' ', ch.ch))
	}
	return sb.String()
}

func TestAltitudeFormatFieldB(t *testing.T) {
	for _, c := range []struct {
		name string
		fp   sim.FlightPlan
		alt  float32
		want string
	}{
		{"at assigned", sim.FlightPlan{AssignedAltitude: 30000}, 30000, "300C"},
		{"climbing", sim.FlightPlan{AssignedAltitude: 30000}, 25300, "300" + upArrow + "253"},
		{"descending", sim.FlightPlan{AssignedAltitude: 23000}, 25300, "230" + downArrow + "253"},
		{"interim", sim.FlightPlan{AssignedAltitude: 30000, InterimAlt: 10000}, 25300, "100T253"},
		{"local interim over procedure", sim.FlightPlan{AssignedAltitude: 30000, InterimAlt: 18000,
			InterimType: sim.InterimProcedure, LocalInterimAlt: 20000}, 25300, "200L253"},
		{"above block", sim.FlightPlan{AltitudeBlock: [2]int{20000, 25000}}, 35300, "200B353"},
		{"within block", sim.FlightPlan{AltitudeBlock: [2]int{20000, 25000}}, 22100, "200B250"},
		{"block ceiling", sim.FlightPlan{AltitudeBlock: [2]int{20000, 25000}}, 25040, "200B250"},
		{"below block", sim.FlightPlan{AltitudeBlock: [2]int{20000, 25000}}, 19300, "200B193"},
		{"VFR", sim.FlightPlan{AssignedAltitudeQualifier: sim.VFRAltitude}, 5500, "VFR/055"},
		{"VFR at an altitude", sim.FlightPlan{AssignedAltitude: 10500, AssignedAltitudeQualifier: sim.VFRAltitude},
			5500, "VFR/055"},
		{"VFR-on-top", sim.FlightPlan{AssignedAltitudeQualifier: sim.OTPAltitude}, 21800, "OTP/218"},
	} {
		trk := sim.Track{RadarTrack: av.RadarTrack{ADSBCallsign: "AAL1"}, FlightPlan: &c.fp}
		ep := &Scope{TrackState: map[av.ADSBCallsign]*TrackState{
			"AAL1": {Track: av.RadarTrack{TransponderAltitude: c.alt}},
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

// With TYPE selected, line 4 shows the aircraft type and equipment suffix.
func TestLine4AircraftData(t *testing.T) {
	h := makeTrackTestHarness()
	h.ep.currentPrefs().Line4Type = Line4Type
	trk := &sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "AAL1", Mode: av.TransponderModeAltitude,
			TransponderAltitude: 20000},
		FlightPlan: &sim.FlightPlan{ACID: "AAL1", AircraftType: "B738", OwningTCW: "TCW1"},
	}
	h.addTrack(trk)
	h.frame(0)
	for suffix, want := range map[string]string{"L": "B738/L", "": "B738"} {
		trk.FlightPlan.EquipmentSuffix = suffix
		fdb := h.ep.getDatablock(h.ctx, *trk, FullDatablock, colors.yellow).(*fullDatablock)
		if got := dbText(fdb.line4[:]); got != want {
			t.Errorf("suffix %q: got line 4 %q, want %q", suffix, got, want)
		}
	}
}

// In Field E, only the 7600 and 7700 special code texts blink; handoff
// text time-shares with the ground speed without blinking.
func TestFieldEBlinking(t *testing.T) {
	h := makeTrackTestHarness()
	h.ctx.Client.State.Controllers = map[sim.ControlPosition]*av.Controller{"2B": {Position: "2B"}}
	trk := &sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "AAL1", Mode: av.TransponderModeAltitude,
			TransponderAltitude: 20000, Groundspeed: 420},
		FlightPlan: &sim.FlightPlan{ACID: "AAL1", OwningTCW: "TCW1"},
	}
	h.addTrack(trk)

	fieldE := func() (string, bool) {
		h.frame(0)
		fdb := h.ep.getDatablock(h.ctx, *trk, FullDatablock, colors.yellow).(*fullDatablock)
		return dbText(fdb.fieldE[:]), fdb.fieldE[0].flashing
	}
	for _, c := range []struct {
		squawk av.Squawk
		text   string
		blink  bool
	}{
		{0o7700, "EMRG", true},
		{0o7600, "RDOF", true},
		{0o7500, "HIJK", false},
		{0o7400, "LLNK", false},
	} {
		trk.Squawk = c.squawk
		h.ep.lastRadarUpdate = sim.Time{}
		if text, blink := fieldE(); text != c.text || blink != c.blink {
			t.Errorf("%s: got %q blinking %v, want %q blinking %v", c.squawk, text, blink, c.text, c.blink)
		}
	}

	trk.Squawk = 0o2301
	h.ep.lastRadarUpdate = sim.Time{}
	trk.FlightPlan.HandoffController = "2B"
	for alternate, want := range map[bool]string{true: "H-2B", false: " 420"} {
		h.ep.dbAlternate = alternate
		h.ep.dbLastAlternateTime = h.start
		if text, blink := fieldE(); text != want || blink {
			t.Errorf("handoff: got %q blinking %v, want %q steady", text, blink, want)
		}
	}
}

// The HSF indicator ends line 3, after Field E, rather than line 2.
func TestHSFIndicatorPosition(t *testing.T) {
	h := makeTrackTestHarness()
	trk := &sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "AAL1", Mode: av.TransponderModeAltitude,
			TransponderAltitude: 20000, Groundspeed: 420},
		FlightPlan: &sim.FlightPlan{ACID: "AAL1", CID: "123", AssignedAltitude: 20000, Scratchpad: "250",
			OwningTCW: "TCW1"},
	}
	h.addTrack(trk)
	h.frame(0)
	var lines [5]dbLine
	fullDatablockLines(h.ep.getDatablock(h.ctx, *trk, FullDatablock, colors.yellow).(*fullDatablock), &lines)
	text := func(l dbLine) string { return dbText(l.ch[:l.length]) }
	if got, want := text(lines[2]), "  200C"; got != want {
		t.Errorf("line 2: got %q, want %q", got, want)
	}
	if got, want := text(lines[3]), "  123 420"+hsfIndicator; got != want {
		t.Errorf("line 3: got %q, want %q", got, want)
	}
}

// LINE 4 sets line 4's font up to two sizes smaller than the FDB font, the
// largest size standing for the larger FDB sizes (3.7), and line 4's
// outlines follow its font.
func TestLine4FontSize(t *testing.T) {
	ep := &Scope{prefSet: &PrefrenceSet{Current: *makeDefaultPreferences()}}
	for i := range ep.systemFont {
		ep.systemFont[i] = &renderer.Font{Size: 10 + i}
	}
	ps := ep.currentPrefs()
	for _, tc := range []struct{ fdb, line4, want int }{{3, 0, 3}, {3, -1, 2}, {2, -2, 1}, {5, -1, 3}} {
		ps.FDBSize, ps.Line4Size = tc.fdb, tc.line4
		if f := ep.fdbLine4Font(); f != ep.ERAMFont(tc.want) {
			t.Errorf("FDB %d LINE 4 %d: line 4 has size %d font, want size %d", tc.fdb, tc.line4, f.Size, tc.want)
		}
	}

	l := DatablockLayout{CharWidth: 10, LineHeight: 16, LinePitch: 22, Line4CharWidth: 8, Line4Height: 12}
	cid := l.FieldExtent(DatablockFieldSpec{Line: dbCIDLine, Col: 2, Cols: 3})
	speed := l.FieldExtent(DatablockFieldSpec{Line: dbLine4, Col: 1, Cols: 3})
	if cid.Width() != 30 || cid.Height() != 16 {
		t.Errorf("CID field is %v", cid)
	}
	if speed.P0[0] != 8 || speed.Width() != 24 || speed.Height() != 12 || speed.P1[1] != l.lineTop(dbLine4) {
		t.Errorf("line 4 field is %v", speed)
	}
}

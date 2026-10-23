// eram/datablock_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
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
	ep := &Scope{prefSet: &PreferenceSet{Current: *makeDefaultPreferences()}}
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
		fdb := h.ep.getDatablock(h.ctx, *trk, FullDatablock).(*fullDatablock)
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
		fdb := h.ep.getDatablock(h.ctx, *trk, FullDatablock).(*fullDatablock)
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
	fullDatablockLines(h.ep.getDatablock(h.ctx, *trk, FullDatablock).(*fullDatablock), &lines)
	text := func(l dbLine) string { return dbText(l.ch[:l.length]) }
	if got, want := text(lines[2]), "  200C"; got != want {
		t.Errorf("line 2: got %q, want %q", got, want)
	}
	if got, want := text(lines[3]), "  123 420"+hsfIndicator; got != want {
		t.Errorf("line 3: got %q, want %q", got, want)
	}
}

// PORTAL and LINE 4 set the fonts of the fields outside the portal fence
// and of line 4 up to two sizes smaller than the FDB font, the largest size
// standing for the larger FDB sizes (3.7). Their outlines follow their
// fonts, next to the rest of the datablock.
func TestFDBFontSizes(t *testing.T) {
	ep := &Scope{prefSet: &PreferenceSet{Current: *makeDefaultPreferences()}}
	for i := range ep.systemFont {
		ep.systemFont[i] = &renderer.Font{Size: 10 + i}
	}
	ps := ep.currentPrefs()
	for _, tc := range []struct{ fdb, relative, want int }{{3, 0, 3}, {3, -1, 2}, {2, -2, 1}, {5, -1, 3}} {
		ps.FDBSize, ps.PortalSize, ps.Line4Size = tc.fdb, tc.relative, tc.relative
		if f := ep.fdbPortalFont(); f != ep.ERAMFont(tc.want) {
			t.Errorf("FDB %d PORTAL %d: size %d font, want size %d", tc.fdb, tc.relative, f.Size, tc.want)
		}
		if f := ep.fdbLine4Font(); f != ep.ERAMFont(tc.want) {
			t.Errorf("FDB %d LINE 4 %d: size %d font, want size %d", tc.fdb, tc.relative, f.Size, tc.want)
		}
	}

	l := DatablockLayout{CharWidth: 10, LineHeight: 16, LinePitch: 22, Line0CharWidth: 7, Line0Height: 10,
		Line4CharWidth: 8, Line4Height: 12}
	cid := l.FieldExtent(DatablockFieldSpec{Line: dbCIDLine, Col: 2, Cols: 3})
	pointOut := l.FieldExtent(DatablockFieldSpec{Line: 0, Col: 2, Cols: 1})
	speed := l.FieldExtent(DatablockFieldSpec{Line: dbLine4, Col: 1, Cols: 3})
	if cid.Width() != 30 || cid.Height() != 16 {
		t.Errorf("CID field is %v", cid)
	}
	if pointOut.P0[0] != 14 || pointOut.Width() != 7 || pointOut.Height() != 10 ||
		pointOut.P0[1] != l.lineTop(0)-l.LineHeight {
		t.Errorf("line 0 field is %v", pointOut)
	}
	if speed.P0[0] != 8 || speed.Width() != 24 || speed.Height() != 12 || speed.P1[1] != l.lineTop(dbLine4) {
		t.Errorf("line 4 field is %v", speed)
	}
}

// While VRI is held, FDBs show the vertical rate after the altitude, in
// hundreds of feet per minute (5.1.5.13). While CODE is held, Field E and
// LDB line 3 show the beacon code, the latter only where line 1 doesn't
// (5.1.5.5); while SPEED is held, Field E shows the ground speed (5.1.5.6).
func TestDatablockOverrides(t *testing.T) {
	h := makeTrackTestHarness()
	h.ctx.Client.State.Controllers = map[sim.ControlPosition]*av.Controller{"2B": {Position: "2B"}}
	trk := &sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "AAL1", Mode: av.TransponderModeAltitude, Squawk: 0o4521,
			TransponderAltitude: 20000, Groundspeed: 420},
		FlightPlan: &sim.FlightPlan{ACID: "AAL1", CID: "123", AssignedAltitude: 30000, OwningTCW: "TCW1",
			HandoffController: "2B"},
	}
	unpaired := &sim.Track{RadarTrack: av.RadarTrack{ADSBCallsign: "N12", Mode: av.TransponderModeAltitude,
		Squawk: 0o1200, TransponderAltitude: 5500}}
	h.addTrack(trk)
	h.addTrack(unpaired)
	h.frame(0)
	trk.TransponderAltitude = 20300
	h.frame(eramUpdateInterval)
	h.ep.dbAlternate = true
	hold := func(o dbOverride) { h.ep.toolbar.press = toolbarPress{down: true, override: o} }
	fdb := func() *fullDatablock {
		return h.ep.getDatablock(h.ctx, *trk, FullDatablock).(*fullDatablock)
	}
	ldb := func(trk *sim.Track) *limitedDatablock {
		return h.ep.getDatablock(h.ctx, *trk, LimitedDatablock).(*limitedDatablock)
	}

	if got := dbText(fdb().line2[:]); got != "300"+upArrow+"203" {
		t.Errorf("line 2 without VRI held: %q", got)
	}
	hold(overrideVRI)
	if got := dbText(fdb().line2[:]); got != "300"+upArrow+"203 +15" {
		t.Errorf("line 2 with VRI held: %q", got)
	}

	hold(overrideCode)
	if got := dbText(fdb().fieldE[:]); got != "4521" {
		t.Errorf("Field E with CODE held: %q", got)
	}
	if got := dbText(ldb(trk).line2[:]); got != "4521" {
		t.Errorf("paired LDB line 3 with CODE held: %q", got)
	}
	if got := dbText(ldb(unpaired).line2[:]); got != "" {
		t.Errorf("unpaired LDB showing its code on line 1 has line 3 %q with CODE held", got)
	}

	hold(overrideSpeed)
	if got := dbText(fdb().fieldE[:]); got != " 420" {
		t.Errorf("Field E with SPEED held during a handoff: %q", got)
	}
	hold(noDBOverride)
	if got := dbText(fdb().fieldE[:]); got != "H-2B" {
		t.Errorf("Field E with nothing held during a handoff: %q", got)
	}
}

// dwellTestHarness has an owned track AAL1 with an FDB, another sector's
// track DAL2, and an unpaired VFR, N12, with LDBs, on a scope with the ERAM
// fonts.
type dwellTestHarness struct {
	*trackTestHarness
	transforms scope.Transformations
}

func makeDwellTestHarness() dwellTestHarness {
	h := makeTrackTestHarness()
	fonts := scope.CreateERAMFonts(fakeRenderer{}, 1)
	for i := range h.ep.systemFont {
		h.ep.systemFont[i] = scope.FindERAMFont(fonts, "EramText-11.pcf", 13)
	}
	h.ep.systemFont[9] = scope.FindERAMFont(fonts, "EramTracks-16.pcf", 15)
	h.ctx.DrawExtent = math.Extent2D{P1: [2]float32{1000, 1000}}
	h.ctx.DrawPixelScale = 1
	h.ctx.Mouse = &platform.MouseState{}

	for _, trk := range []*sim.Track{
		{RadarTrack: av.RadarTrack{ADSBCallsign: "AAL1", Mode: av.TransponderModeAltitude, Squawk: 0o4521,
			TransponderAltitude: 20000, Location: math.Point2LL{-77, 39}},
			FlightPlan: &sim.FlightPlan{ACID: "AAL1", CID: "123", AssignedAltitude: 20000, OwningTCW: "TCW1"}},
		{RadarTrack: av.RadarTrack{ADSBCallsign: "DAL2", Mode: av.TransponderModeAltitude, Squawk: 0o4522,
			TransponderAltitude: 30000, Location: math.Point2LL{-77.3, 39}},
			FlightPlan: &sim.FlightPlan{ACID: "DAL2", CID: "456", AssignedAltitude: 30000, OwningTCW: "TCW2"}},
		{RadarTrack: av.RadarTrack{ADSBCallsign: "N12", Mode: av.TransponderModeAltitude, Squawk: 0o1200,
			TransponderAltitude: 5500, Location: math.Point2LL{-76.7, 39}}},
	} {
		h.addTrack(trk)
	}
	h.frame(0)
	return dwellTestHarness{trackTestHarness: h,
		transforms: scope.GetTransformations(h.ctx.DrawExtent, 45, math.Point2LL{-77, 39}, 50, 0)}
}

// hover runs the frame's dwell update with the cursor at p.
func (h dwellTestHarness) hover(p [2]float32) av.ADSBCallsign {
	h.ctx.Mouse.Pos = p
	h.ep.updateDwell(h.ctx, h.ep.visibleTracks, h.transforms)
	return h.ep.dwellTrack
}

// textCenter returns the center of the text of a track's data block.
func (h dwellTestHarness) textCenter(callsign av.ADSBCallsign) [2]float32 {
	db, l, ok := h.ep.emphasizableDatablock(h.ctx, *h.ctx.Client.State.Tracks[callsign], h.transforms)
	if !ok {
		panic(string(callsign) + " has no data block that can be emphasized")
	}
	return db.textExtent(l).Center()
}

// The cursor dwells on an FDB's text or its track position symbol, or on a
// paired LDB, but not on an unpaired LDB (5.1.5.8).
func TestDwellTargets(t *testing.T) {
	h := makeDwellTestHarness()
	track := func(callsign av.ADSBCallsign) [2]float32 {
		return h.transforms.WindowFromLatLongP(h.ctx.Client.State.Tracks[callsign].Location)
	}

	if got := h.hover(h.textCenter("AAL1")); got != "AAL1" {
		t.Errorf("cursor on the FDB: dwell on %q", got)
	}
	if got := h.hover(track("AAL1")); got != "AAL1" {
		t.Errorf("cursor on the FDB's track position symbol: dwell on %q", got)
	}
	if got := h.hover(h.textCenter("DAL2")); got != "DAL2" {
		t.Errorf("cursor on the paired LDB: dwell on %q", got)
	}
	if got := h.hover(track("DAL2")); got != "" {
		t.Errorf("cursor on the paired LDB's target: dwell on %q", got)
	}

	ldb := h.ep.getDatablock(h.ctx, *h.ctx.Client.State.Tracks["N12"], LimitedDatablock)
	anchor, _ := h.ep.datablockAnchor(h.ctx, *h.ctx.Client.State.Tracks["N12"], ldb, LimitedDatablock, h.transforms)
	font := h.ep.ERAMFont(h.ep.currentPrefs().LDBSize)
	unpaired := ldb.textExtent(makeDatablockLayout(anchor, font, font, font))
	if unpaired.IsEmpty() {
		t.Fatalf("unpaired LDB has no text")
	}
	if got := h.hover(unpaired.Center()); got != "" {
		t.Errorf("cursor on the unpaired LDB: dwell on %q", got)
	}
}

// Where data blocks overlap, dwell emphasis goes to the one it is already
// on, and otherwise to an FDB over an LDB.
func TestDwellOverlap(t *testing.T) {
	h := makeDwellTestHarness()
	db, l, _ := h.ep.emphasizableDatablock(h.ctx, *h.ctx.Client.State.Tracks["AAL1"], h.transforms)
	fdb := db.textExtent(l)
	// Center DAL2's LDB on the right edge of AAL1's FDB.
	dal2 := h.ctx.Client.State.Tracks["DAL2"]
	offset := math.Sub2f([2]float32{fdb.P1[0], fdb.Center()[1]}, h.textCenter("DAL2"))
	dal2.Location = h.transforms.LatLongFromWindowP(math.Add2f(h.transforms.WindowFromLatLongP(dal2.Location), offset))
	h.frame(eramUpdateInterval)

	y := fdb.Center()[1]
	ldbOnly, overlap, fdbOnly := [2]float32{fdb.P1[0] + 15, y}, [2]float32{fdb.P1[0] - 10, y}, [2]float32{fdb.P0[0] + 5, y}
	for _, step := range []struct {
		p    [2]float32
		want av.ADSBCallsign
	}{{overlap, "AAL1"}, {ldbOnly, "DAL2"}, {overlap, "DAL2"}, {fdbOnly, "AAL1"}, {overlap, "AAL1"}} {
		if got := h.hover(step.p); got != step.want {
			t.Errorf("cursor at %v: dwell on %q, want %q", step.p, got, step.want)
		}
	}
}

// Pressing a trackball button removes dwell emphasis until the cursor
// moves (5.1.5.8).
func TestDwellRemovedByButtonPress(t *testing.T) {
	h := makeDwellTestHarness()
	p := h.textCenter("AAL1")
	h.ctx.Mouse.Down[platform.MouseButtonTertiary] = true
	if got := h.hover(p); got != "" {
		t.Errorf("button held: dwell on %q", got)
	}
	h.ctx.Mouse.Down[platform.MouseButtonTertiary] = false
	if got := h.hover(p); got != "" {
		t.Errorf("button released, cursor still: dwell on %q", got)
	}
	if got := h.hover(math.Add2f(p, [2]float32{1, 0})); got != "AAL1" {
		t.Errorf("cursor moved: dwell on %q", got)
	}
}

// Dwell emphasis adds DWELL's brightness to a data block's text and to its
// leader line and vector line, track position symbol, and RDB (5.1.5.8).
func TestDwellBrightness(t *testing.T) {
	h := makeDwellTestHarness()
	ps := h.ep.currentPrefs()
	aal1, dal2 := *h.ctx.Client.State.Tracks["AAL1"], *h.ctx.Client.State.Tracks["DAL2"]
	fdbColor := func() renderer.RGB {
		return h.ep.getDatablock(h.ctx, aal1, FullDatablock).(*fullDatablock).line1[0].color
	}
	ldbColor := func() renderer.RGB {
		return h.ep.getDatablock(h.ctx, dal2, LimitedDatablock).(*limitedDatablock).line0[0].color
	}

	h.hover(h.textCenter("AAL1"))
	emphasized := (ps.Brightness.FDB + ps.Brightness.Dwell).ScaleRGB(colors.yellow)
	if got := fdbColor(); got != emphasized {
		t.Errorf("dwelled FDB: color %v, want %v", got, emphasized)
	}
	if got := h.ep.trackDatablockColor(h.ctx, aal1); got != emphasized {
		t.Errorf("dwelled FDB's leader line: color %v, want %v", got, emphasized)
	}
	if got, want := h.ep.dwellBrightness(aal1), ps.Brightness.Dwell; got != want {
		t.Errorf("dwelled FDB: track symbol and RDB brightened by %d, want %d", got, want)
	}
	if got, want := ldbColor(), ps.Brightness.LDB.ScaleRGB(colors.yellow); got != want {
		t.Errorf("LDB of another track: color %v, want %v", got, want)
	}

	h.hover(h.textCenter("DAL2"))
	if got, want := ldbColor(), (ps.Brightness.LDB + ps.Brightness.Dwell).ScaleRGB(colors.yellow); got != want {
		t.Errorf("dwelled LDB: color %v, want %v", got, want)
	}
	if got, want := fdbColor(), ps.Brightness.FDB.ScaleRGB(colors.yellow); got != want {
		t.Errorf("FDB of another track: color %v, want %v", got, want)
	}
}

// Picking Field A of an FDB or a paired LDB locks dwell emphasis on it until
// it is picked again, unless a command is being entered (5.1.5.9).
func TestUserEmphasis(t *testing.T) {
	h := makeDwellTestHarness()
	// pick picks a track's Field A and reports whether the pick was taken.
	pick := func(callsign av.ADSBCallsign) bool {
		trk := *h.ctx.Client.State.Tracks[callsign]
		db, l, _ := h.ep.emphasizableDatablock(h.ctx, trk, h.transforms)
		h.ctx.Mouse.Pos = db.fieldAExtent(l).Center()
		h.ctx.Mouse.Clicked[platform.MouseButtonPrimary] = true
		h.ep.datablockInteractions(h.ctx, h.ep.visibleTracks, h.transforms)
		return !h.ctx.Mouse.Clicked[platform.MouseButtonPrimary]
	}
	emphasized := func(callsign av.ADSBCallsign) bool {
		return h.ep.emphasized(*h.ctx.Client.State.Tracks[callsign])
	}

	for _, callsign := range []av.ADSBCallsign{"AAL1", "DAL2"} {
		if !pick(callsign) {
			t.Errorf("%s: pick on Field A not taken", callsign)
		}
		h.hover([2]float32{0, 0})
		if !emphasized(callsign) {
			t.Errorf("%s: not emphasized after picking Field A and moving the cursor away", callsign)
		}
		pick(callsign)
		if emphasized(callsign) {
			t.Errorf("%s: still emphasized after picking Field A again", callsign)
		}

		h.ep.input.Set("QP 2B")
		if pick(callsign) || emphasized(callsign) {
			t.Errorf("%s: pick on Field A taken while a command is entered", callsign)
		}
		h.ep.input.Clear()
	}
}

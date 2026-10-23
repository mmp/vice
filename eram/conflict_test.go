// eram/conflict_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"slices"
	"strings"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/sim"

	"github.com/AllenDang/cimgui-go/imgui"
)

func TestCAAltitudeEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tgt    caTarget
		t      float32
		lo, hi float32
	}{
		// Climbing toward the data block altitude: extrapolate then cap.
		{"climb below cap", caTarget{alt: 30000, rate: 2000, dbAlt: 34000}, 60, 32000, 32000},
		{"climb capped", caTarget{alt: 30000, rate: 2000, dbAlt: 34000}, 180, 34000, 34000},
		// Descending toward the data block altitude: cap at level-off.
		{"descent capped", caTarget{alt: 37000, rate: -1500, dbAlt: 35000}, 240, 35000, 35000},
		// Descending away from a DB altitude that is above: no cap.
		{"descent away from db", caTarget{alt: 30000, rate: -1000, dbAlt: 34000}, 240, 26000, 26000},
		// Climbing with no DB altitude: pure extrapolation.
		{"climb no db", caTarget{alt: 30000, rate: 1000, dbAlt: 0}, 240, 34000, 34000},
		// Climbing away from a DB altitude that is below: no cap.
		{"climb away from db", caTarget{alt: 30000, rate: 1000, dbAlt: 26000}, 240, 34000, 34000},
		// Level at the DB altitude: point envelope.
		{"level at db", caTarget{alt: 34000, rate: 0, dbAlt: 34000}, 120, 34000, 34000},
		// Level with DB altitude below: the band grows toward it at the
		// maximum rate.
		{"level band below now", caTarget{alt: 34000, rate: 0, dbAlt: 30000}, 0, 34000, 34000},
		{"level band below", caTarget{alt: 34000, rate: 0, dbAlt: 30000}, 60, 31500, 34000},
		{"level band below later", caTarget{alt: 34000, rate: 0, dbAlt: 30000}, 240, 30000, 34000},
		// Level with DB altitude above: band up toward the DB altitude.
		{"level band above", caTarget{alt: 30000, rate: 0, dbAlt: 33000}, 60, 30000, 32500},
		// Level with no DB altitude: point envelope.
		{"level no db", caTarget{alt: 31000, rate: 0, dbAlt: 0}, 240, 31000, 31000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lo, hi := caAltitudeEnvelope(tc.tgt, tc.t)
			if lo != tc.lo || hi != tc.hi {
				t.Errorf("got [%v, %v], want [%v, %v]", lo, hi, tc.lo, tc.hi)
			}
		})
	}
}

func TestCATargetPosition(t *testing.T) {
	// 0.1 NM/s east, with a route that goes 3 nm east and then turns north.
	tgt := caTarget{pos: [2]float32{0, 0}, vel: [2]float32{0.1, 0}, route: [][2]float32{{3, 0}, {3, 3}}}
	for _, tc := range []struct {
		t    float32
		want [2]float32
	}{
		{0, [2]float32{0, 0}},
		{10, [2]float32{1, 0}},
		{30, [2]float32{3, 0}},
		{45, [2]float32{3, 1.5}},
		// Past the end of the route, it carries on along the last leg.
		{90, [2]float32{3, 6}},
	} {
		if p := tgt.position(tc.t); math.Length2f(math.Sub2f(p, tc.want)) > 1e-4 {
			t.Errorf("at %vs: got %v, want %v", tc.t, p, tc.want)
		}
	}

	// Without a route, it holds its track.
	tgt.route = nil
	if p := tgt.position(90); math.Length2f(math.Sub2f(p, [2]float32{9, 0})) > 1e-4 {
		t.Errorf("without a route: got %v, want [9 0]", p)
	}
}

func TestCAIntervalGap(t *testing.T) {
	if g := caIntervalGap(30000, 34000, 33000, 33000); g != 0 {
		t.Errorf("overlapping bands: got gap %v, want 0", g)
	}
	if g := caIntervalGap(30000, 32000, 33000, 33000); g != 1000 {
		t.Errorf("bands 1000 apart: got gap %v, want 1000", g)
	}
	if g := caIntervalGap(35000, 35000, 33000, 34000); g != 1000 {
		t.Errorf("reversed order bands: got gap %v, want 1000", g)
	}
}

func TestCAConflictHeadOn(t *testing.T) {
	// 360 kt = 0.1 NM/s. Head-on, co-altitude.
	a := caTarget{pos: [2]float32{0, 0}, vel: [2]float32{0.1, 0}, alt: 35000, rate: 0, dbAlt: 35000}
	b := caTarget{pos: [2]float32{50, 0}, vel: [2]float32{-0.1, 0}, alt: 35000, rate: 0, dbAlt: 35000}
	if !caConflict(a, b) {
		t.Error("head-on pair closing to <5nm within 4 min: want conflict")
	}
	// Too far apart: closest approach at t=240 is 55-48=7 nm.
	b.pos = [2]float32{55, 0}
	if caConflict(a, b) {
		t.Error("pair that stays >5nm apart for the full window: want no conflict")
	}
}

func TestCAConflictDataBlockAltitudeCap(t *testing.T) {
	// Spec example: A at FL370 descending with DB alt FL350; B level FL330
	// directly ahead. A levels at FL350 -> 2000 ft gap -> no alert.
	a := caTarget{pos: [2]float32{0, 0}, vel: [2]float32{0.1, 0}, alt: 37000, rate: -1500, dbAlt: 35000}
	b := caTarget{pos: [2]float32{1, 0}, vel: [2]float32{0.1, 0}, alt: 33000, rate: 0, dbAlt: 33000}
	if caConflict(a, b) {
		t.Error("descent capped at FL350 vs level FL330: want no conflict")
	}
	// No DB altitude: A descends through B's altitude -> alert.
	a.dbAlt = 0
	if !caConflict(a, b) {
		t.Error("uncapped descent through FL330: want conflict")
	}
	// DB altitude exactly 1000 ft above B: still separated -> no alert.
	a.dbAlt = 34000
	if caConflict(a, b) {
		t.Error("descent capped exactly 1000 ft above: want no conflict")
	}
}

func TestCAConflictLevelBand(t *testing.T) {
	// Spec example: A level FL350 with a lower DB altitude, B level FL340.
	// A may start down at any time -> alert.
	a := caTarget{pos: [2]float32{0, 0}, vel: [2]float32{0.1, 0}, alt: 35000, rate: 0, dbAlt: 33000}
	b := caTarget{pos: [2]float32{1, 0}, vel: [2]float32{0.1, 0}, alt: 34000, rate: 0, dbAlt: 34000}
	if !caConflict(a, b) {
		t.Error("level FL350 with DB alt below vs level FL340: want conflict")
	}
	// DB altitude matches current altitude on both -> no alert.
	a.dbAlt = 35000
	if caConflict(a, b) {
		t.Error("both level at their DB altitudes, 1000 ft apart: want no conflict")
	}
}

func TestCAConflictLevelBandTooFarToDescend(t *testing.T) {
	// A is level at 11,000 with 3,100 in its data block and passes over B,
	// which is climbing out underneath it. They are within 3 nm only until
	// t=46s, by when A could have descended no lower than 9,083.
	a := caTarget{pos: [2]float32{0, 0}, vel: [2]float32{0.08, 0}, alt: 11000, rate: 0, dbAlt: 3100}
	b := caTarget{pos: [2]float32{3, 0}, vel: [2]float32{-0.05, 0}, alt: 1000, rate: 2500, dbAlt: 10000}
	if caConflict(a, b) {
		t.Error("level arrival 10,000 feet above a departure it passes over: want no conflict")
	}
}

func TestCAConflictReducedSeparation(t *testing.T) {
	// Parallel tracks 4 nm apart, constant separation.
	mk := func(alt float32, y float32) caTarget {
		return caTarget{pos: [2]float32{0, y}, vel: [2]float32{0.1, 0}, alt: alt, rate: 0, dbAlt: int(alt)}
	}
	// At/below FL230 the minimum is 3 nm -> 4 nm apart is fine.
	if caConflict(mk(20000, 0), mk(20000, 4)) {
		t.Error("4nm lateral at FL200 (3nm minimum applies): want no conflict")
	}
	// Above FL230 the minimum is 5 nm -> 4 nm apart alerts.
	if !caConflict(mk(24000, 0), mk(24000, 4)) {
		t.Error("4nm lateral at FL240 (5nm minimum applies): want conflict")
	}
	// Within 3 nm at/below FL230 alerts.
	if !caConflict(mk(20000, 0), mk(20000, 2.5)) {
		t.Error("2.5nm lateral at FL200: want conflict")
	}
}

func TestMergeCAPairs(t *testing.T) {
	ab := [2]av.ADSBCallsign{"AAL1", "DAL2"}
	cd := [2]av.ADSBCallsign{"SWA3", "UAL4"}
	t0, t1 := sim.Time{}, sim.Time{}.Add(5*time.Second)

	prev := []CAPair{{ADSBCallsigns: ab, Start: t0}}

	// AB persists with its original start time; CD is added with now.
	merged := mergeCAPairs(prev, [][2]av.ADSBCallsign{ab, cd}, t1)
	if len(merged) != 2 {
		t.Fatalf("got %d pairs, want 2", len(merged))
	}
	if merged[0].ADSBCallsigns != ab || !merged[0].Start.Equal(t0) {
		t.Errorf("existing pair should keep its start time: got %+v", merged[0])
	}
	if merged[1].ADSBCallsigns != cd || !merged[1].Start.Equal(t1) {
		t.Errorf("new pair should start now: got %+v", merged[1])
	}

	// AB no longer detected: dropped.
	merged = mergeCAPairs(merged, [][2]av.ADSBCallsign{cd}, t1)
	if len(merged) != 1 || merged[0].ADSBCallsigns != cd {
		t.Errorf("stale pair should be dropped: got %+v", merged)
	}
}

func TestInConflictAlert(t *testing.T) {
	ep := &Scope{caPairs: []CAPair{
		{ADSBCallsigns: [2]av.ADSBCallsign{"AAL1", "DAL2"}},
	}}
	if !ep.inConflictAlert("AAL1") || !ep.inConflictAlert("DAL2") {
		t.Error("both pair members should be in conflict alert")
	}
	if ep.inConflictAlert("SWA3") {
		t.Error("non-member should not be in conflict alert")
	}
}

// TestCATRACONDataBlockAltitude checks that a departure a TRACON holds below
// traffic doesn't alert just because its data block shows its filed altitude.
// TRACON controllers can't make data block entries, so that altitude doesn't
// say where the departure will level off.
func TestCATRACONDataBlockAltitude(t *testing.T) {
	for _, tc := range []struct {
		owner sim.ControlPosition
		alert bool
	}{
		{owner: "N5W", alert: false},
		{owner: "2B", alert: true},
	} {
		t.Run(string(tc.owner), func(t *testing.T) {
			h := makeTrackTestHarness()
			h.ctx.NmPerLongitude = 45.7
			h.ctx.Client.State.Controllers = map[sim.ControlPosition]*av.Controller{
				"1A":  {Position: "1A", ERAMFacility: true},
				"2B":  {Position: "2B", ERAMFacility: true},
				"N5W": {Position: "5W", FacilityIdentifier: "N"},
			}
			// The departure is level at 17,000 with FL380 filed and the
			// traffic is level at 19,000 a mile ahead of it.
			dep := &sim.Track{
				RadarTrack: av.RadarTrack{ADSBCallsign: "DEP", Mode: av.TransponderModeAltitude,
					TransponderAltitude: 17000, Location: math.Point2LL{-73, 40}, Groundspeed: 400},
				FlightPlan:          &sim.FlightPlan{ACID: "DEP", TrackingController: tc.owner, AssignedAltitude: 38000},
				VirtuallyControlled: true,
			}
			traffic := &sim.Track{
				RadarTrack: av.RadarTrack{ADSBCallsign: "ENR", Mode: av.TransponderModeAltitude,
					TransponderAltitude: 19000, Location: math.Point2LL{-72.98, 40}, Groundspeed: 400},
				FlightPlan: &sim.FlightPlan{ACID: "ENR", TrackingController: "1A", AssignedAltitude: 19000},
			}
			h.addTrack(dep)
			h.addTrack(traffic)
			h.frame(0)
			// Both fly east until the next radar sample gives them headings.
			dep.Location[0] += 0.03
			traffic.Location[0] += 0.03
			h.frame(eramUpdateInterval)

			h.ep.updateConflictAlerts(h.ctx, h.ep.visibleTracks)
			if got := h.ep.inConflictAlert("DEP"); got != tc.alert {
				t.Errorf("conflict alert: got %v, expected %v", got, tc.alert)
			}
		})
	}
}

// A new conflict pair displays the Conflict Alert view, which lists each
// pair with the sector owning each track the user doesn't. A suppressed
// pair's data blocks stop blinking, and the view can't be suppressed while
// it has entries (4.2.5).
func TestConflictAlertView(t *testing.T) {
	h := makeTrackTestHarness()
	h.ctx.NmPerLongitude = 45.7
	h.ctx.Client.State.Controllers = map[sim.ControlPosition]*av.Controller{
		"1A": {Position: "1A", ERAMFacility: true},
		"2B": {Position: "2B", ERAMFacility: true},
	}
	// Head on at 19,000, 2 miles apart.
	west := &sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "AAL1", Mode: av.TransponderModeAltitude,
			TransponderAltitude: 19000, Location: math.Point2LL{-73.1, 40}, Groundspeed: 400},
		FlightPlan: &sim.FlightPlan{ACID: "AAL1", TrackingController: "1A", OwningTCW: "TCW1", AssignedAltitude: 19000},
	}
	east := &sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "DAL2", Mode: av.TransponderModeAltitude,
			TransponderAltitude: 19000, Location: math.Point2LL{-73, 40}, Groundspeed: 400},
		FlightPlan: &sim.FlightPlan{ACID: "DAL2", TrackingController: "2B", OwningTCW: "TCW2", AssignedAltitude: 19000},
	}
	h.addTrack(west)
	h.addTrack(east)
	h.frame(0)
	west.Location[0] += 0.01
	east.Location[0] -= 0.01
	h.frame(eramUpdateInterval)
	ps := h.ep.currentPrefs()
	h.ep.updateConflictAlerts(h.ctx, h.ep.visibleTracks)
	if len(h.ep.caPairs) != 1 || !ps.ConflictAlert.Visible {
		t.Fatalf("pairs %+v, view displayed %v; want one pair displayed", h.ep.caPairs, ps.ConflictAlert.Visible)
	}
	if got, want := h.ep.conflictAlertEntry(h.ctx, h.ep.caPairs[0]), "  AAL1         DAL2(2B)"; got != want {
		t.Errorf("entry %q, want %q", got, want)
	}

	if err := conflictAlertButton().click(h.ep, toolbarClick{}); err != nil || !ps.ConflictAlert.Visible {
		t.Errorf("CONFLCT ALERT suppressed the view with an entry in it: %v", err)
	}

	h.ep.caPairs[0].Suppressed = true
	if got := h.ep.conflictAlertEntry(h.ctx, h.ep.caPairs[0]); !strings.HasPrefix(got, "X AAL1") {
		t.Errorf("suppressed entry %q has no suppression indicator", got)
	}
	if h.ep.caAlerting("AAL1") || !h.ep.inConflictAlert("AAL1") {
		t.Errorf("a suppressed pair's track: blinking %v, in conflict alert %v; want only the latter",
			h.ep.caAlerting("AAL1"), h.ep.inConflictAlert("AAL1"))
	}
}

// makeGroupSupTestHarness returns a harness with AAL1 and DAL2 head on at
// 19,000 two miles apart, in conflict, and N3 elsewhere; the user owns
// AAL1 and N3.
func makeGroupSupTestHarness(t *testing.T) *trackTestHarness {
	InitCommands()
	h := makeTrackTestHarness()
	h.ctx.NmPerLongitude = 45.7
	h.ctx.Client.State.Controllers = map[sim.ControlPosition]*av.Controller{
		"1A": {Position: "1A", ERAMFacility: true},
		"2B": {Position: "2B", ERAMFacility: true},
	}
	track := func(acid sim.ACID, cid string, lon float32, tracking sim.ControlPosition, tcw sim.TCW) *sim.Track {
		return &sim.Track{
			RadarTrack: av.RadarTrack{ADSBCallsign: av.ADSBCallsign(acid), Mode: av.TransponderModeAltitude,
				TransponderAltitude: 19000, Location: math.Point2LL{lon, 40}, Groundspeed: 400},
			FlightPlan: &sim.FlightPlan{ACID: acid, CID: cid, TrackingController: tracking, OwningTCW: tcw,
				AssignedAltitude: 19000},
		}
	}
	h.addTrack(track("AAL1", "101", -73.1, "1A", "TCW1"))
	h.addTrack(track("DAL2", "102", -73, "2B", "TCW2"))
	h.addTrack(track("N3", "103", -70, "1A", "TCW1"))
	h.frame(0)
	h.ctx.Client.State.Tracks["AAL1"].Location[0] += 0.01
	h.ctx.Client.State.Tracks["DAL2"].Location[0] -= 0.01
	h.frame(eramUpdateInterval)
	return h
}

func (h *trackTestHarness) runSG(t *testing.T, cmd string) error {
	t.Helper()
	_, err, handled := h.ep.tryExecuteUserCommand(h.ctx, cmd, nil, nil)
	if !handled {
		t.Fatalf("%q: not handled", cmd)
	}
	return err
}

// Conflict alerts between the flights of a suppression group are
// suppressed; SG adds and deletes groups and their flights (4.2.14).
func TestSuppressionGroups(t *testing.T) {
	h := makeGroupSupTestHarness(t)
	ps := h.ep.currentPrefs()
	h.ep.updateConflictAlerts(h.ctx, h.ep.visibleTracks)
	if len(h.ep.caPairs) != 1 {
		t.Fatalf("conflict alert pairs %+v, want AAL1 and DAL2", h.ep.caPairs)
	}

	if err := h.runSG(t, "SG A 101/DAL2"); err != nil {
		t.Fatalf("SG A 101/DAL2: %v", err)
	}
	if len(h.ep.SuppressionGroups) != 1 || h.ep.SuppressionGroups[0].ID != 1 ||
		!slices.Equal(h.ep.SuppressionGroups[0].Flights, []sim.ACID{"AAL1", "DAL2"}) || !ps.GroupSup.Visible {
		t.Fatalf("groups %+v, view displayed %v; want group 01 with AAL1 and DAL2, displayed",
			h.ep.SuppressionGroups, ps.GroupSup.Visible)
	}
	h.ep.updateConflictAlerts(h.ctx, h.ep.visibleTracks)
	if len(h.ep.caPairs) != 0 {
		t.Errorf("conflict alert pairs %+v with the pair in a group, want none", h.ep.caPairs)
	}
	if err := groupSupButton().click(h.ep, toolbarClick{}); err != nil || !ps.GroupSup.Visible {
		t.Errorf("GROUP SUP suppressed the view with a group in it: %v", err)
	}

	// Adding and deleting a flight; another sector's flight can't be
	// deleted.
	if err := h.runSG(t, "SG 01 N3"); err != nil {
		t.Fatalf("SG 01 N3: %v", err)
	}
	if err := h.runSG(t, "SG D 01 DAL2"); err != ErrIllegalUserAction {
		t.Errorf("SG D 01 DAL2: %v, want %v", err, ErrIllegalUserAction)
	}
	if err := h.runSG(t, "SG 01 N3"); err != nil {
		t.Fatalf("SG 01 N3 again: %v", err)
	}
	if !slices.Equal(h.ep.SuppressionGroups[0].Flights, []sim.ACID{"AAL1", "DAL2"}) {
		t.Errorf("flights %v after adding and deleting N3", h.ep.SuppressionGroups[0].Flights)
	}
	if err := h.runSG(t, "SG 02 N3"); err != ErrSuppressionGroupNotFound {
		t.Errorf("SG 02 N3: %v, want %v", err, ErrSuppressionGroupNotFound)
	}

	// Entering a group's flights again deletes it, unless with A.
	if err := h.runSG(t, "SG A DAL2/AAL1"); err != nil || len(h.ep.SuppressionGroups) != 1 {
		t.Errorf("SG A DAL2/AAL1: %v, groups %+v; want the group kept", err, h.ep.SuppressionGroups)
	}
	if err := h.runSG(t, "SG DAL2/AAL1"); err != nil || len(h.ep.SuppressionGroups) != 0 {
		t.Errorf("SG DAL2/AAL1: %v, groups %+v; want the group deleted", err, h.ep.SuppressionGroups)
	}
	h.ep.updateConflictAlerts(h.ctx, h.ep.visibleTracks)
	if len(h.ep.caPairs) != 1 {
		t.Errorf("conflict alert pairs %+v after deleting the group, want AAL1 and DAL2", h.ep.caPairs)
	}

	// Unsubscribing deletes a group, as does a flight's track going.
	for _, cmd := range []string{"SG AAL1/N3", "SG DAL2/N3"} {
		if err := h.runSG(t, cmd); err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
	}
	if err := h.runSG(t, "SG D 01"); err != nil {
		t.Fatalf("SG D 01: %v", err)
	}
	if len(h.ep.SuppressionGroups) != 1 || h.ep.SuppressionGroups[0].ID != 2 {
		t.Fatalf("groups %+v, want group 02 alone", h.ep.SuppressionGroups)
	}
	delete(h.ctx.Client.State.Tracks, "N3")
	h.ep.pruneSuppressionGroups(h.ctx)
	if len(h.ep.SuppressionGroups) != 0 {
		t.Errorf("groups %+v after N3's track went, want none", h.ep.SuppressionGroups)
	}
}

// Typing while the view's menu is displayed opens the Group Suppression
// Input Box, whose message is entered as an SG command's (4.2.14.5).
func TestGroupSupInputBox(t *testing.T) {
	h := makeGroupSupTestHarness(t)
	m := &groupSupMenu{}
	p := &viewMenuPopup{menu: m, viewID: "group-sup"}
	h.ep.popup = p
	h.ctx.Keyboard = &platform.KeyboardState{Input: "101/102"}
	if !p.handleKeyboard(h.ep, h.ctx) || !m.typing || m.buf != "101/102" {
		t.Fatalf("after typing: typing %v, buffer %q", m.typing, m.buf)
	}
	h.ctx.Keyboard = &platform.KeyboardState{Pressed: map[imgui.Key]any{imgui.KeyEnter: nil}}
	p.handleKeyboard(h.ep, h.ctx)
	if h.ep.popup != nil || len(h.ep.SuppressionGroups) != 1 {
		t.Errorf("after Enter: popup %v, groups %+v; want the menu closed and a group", h.ep.popup, h.ep.SuppressionGroups)
	}
}

// eram/groupsup_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"slices"
	"testing"

	"github.com/AllenDang/cimgui-go/imgui"
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/sim"
)

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
	p := &groupSupPopup{}
	h.ep.popup = p
	h.ctx.Keyboard = &platform.KeyboardState{Input: "101/102"}
	if !p.handleKeyboard(h.ep, h.ctx) || !p.typing || p.buf != "101/102" {
		t.Fatalf("after typing: typing %v, buffer %q", p.typing, p.buf)
	}
	h.ctx.Keyboard = &platform.KeyboardState{Pressed: map[imgui.Key]any{imgui.KeyEnter: nil}}
	p.handleKeyboard(h.ep, h.ctx)
	if h.ep.popup != nil || len(h.ep.SuppressionGroups) != 1 {
		t.Errorf("after Enter: popup %v, groups %+v; want the menu closed and a group", h.ep.popup, h.ep.SuppressionGroups)
	}
}

// eram/fel_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
)

// The FLIGHT EVENT view lists the call sign mismatches of the user's
// tracks, whose FDBs show the FEL indicator between Field E and the HSF
// indicator; a deleted entry stays deleted until the ACID changes, and the
// view is suppressed when its last entry goes (4.2.13, 5.1.5.19).
func TestFlightEventList(t *testing.T) {
	h := makeTrackTestHarness()
	mismatch := &sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "N123AB", Mode: av.TransponderModeAltitude,
			TransponderAltitude: 5500, Groundspeed: 120},
		FlightPlan: &sim.FlightPlan{ACID: "VFR01", CID: "101", OwningTCW: "TCW1", Scratchpad: "270"},
	}
	others := []*sim.Track{
		{RadarTrack: av.RadarTrack{ADSBCallsign: "AAL1", Mode: av.TransponderModeAltitude},
			FlightPlan: &sim.FlightPlan{ACID: "AAL1", OwningTCW: "TCW1"}},
		{RadarTrack: av.RadarTrack{ADSBCallsign: "N456CD", Mode: av.TransponderModeAltitude},
			FlightPlan: &sim.FlightPlan{ACID: "VFR02", OwningTCW: "TCW2"}},
	}
	h.addTrack(mismatch)
	for _, trk := range others {
		h.addTrack(trk)
	}
	h.frame(0)

	entries := h.ep.felEntries(h.ctx)
	if len(entries) != 1 || entries[0] != mismatch {
		t.Fatalf("FEL entries %v, want N123AB's", entries)
	}
	fdb := h.ep.getDatablock(h.ctx, *mismatch, FullDatablock).(*fullDatablock)
	var lines [5]dbLine
	fullDatablockLines(fdb, &lines)
	if got, want := dbText(lines[3].ch[:lines[3].length]), "  101 120   "+hsfIndicator; got != want {
		t.Errorf("line 3 %q, want %q", got, want)
	}
	l := DatablockLayout{CharWidth: 10, LineHeight: 16, LinePitch: 22}
	if e := fdb.felExtent(l); e.IsEmpty() || e.Width() != 20 {
		t.Errorf("FEL indicator extent %v, want two characters wide", e)
	}

	ps := h.ep.currentPrefs()
	ps.FlightEvent.Visible = true
	h.ep.felHadEntries = true
	h.ep.deleteFELEntry(mismatch)
	if h.ep.felListed(h.ctx, mismatch) {
		t.Error("a deleted entry is listed")
	}
	h.ep.drawFlightEventView(h.ctx, scope.Transformations{}, nil)
	if ps.FlightEvent.Visible {
		t.Error("the view is displayed after its last entry was deleted")
	}
	mismatch.FlightPlan.ACID = "VFR03"
	if !h.ep.felListed(h.ctx, mismatch) {
		t.Error("an entry deleted before its ACID changed isn't listed")
	}
}

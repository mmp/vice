// eram/flightplan_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/sim"
)

func makeCFRTestHarness(n int) *trackTestHarness {
	db.InitDB()
	InitCommands()
	h := makeTrackTestHarness()
	for i := range n {
		cs := av.ADSBCallsign(fmt.Sprintf("AAL%d", i+1))
		h.addTrack(&sim.Track{
			RadarTrack: av.RadarTrack{ADSBCallsign: cs, Mode: av.TransponderModeAltitude, TransponderAltitude: 35000},
			FlightPlan: &sim.FlightPlan{ACID: sim.ACID(cs), CID: fmt.Sprintf("%03d", i+1), TrackingController: "15",
				AircraftType: "B738", EquipmentSuffix: "L", AssignedSquawk: 0o2301, CruiseSpeed: 450,
				AssignedAltitude: 35000, DepartureAirport: "KBOS", Route: "SSOXS SEY PARCH4", ArrivalAirport: "KJFK",
				CoordinationFix: "SEY", CoordinationTime: sim.NewSimTime(h.start)},
		})
	}
	h.frame(0)
	return h
}

// QF readouts go to the CFR view while it is displayed, and to the RA
// otherwise or with QF R (2.10, 4.2.3). The view keeps the newest ENTRIES
// of them and drops those of flights that are gone.
func TestCFREntries(t *testing.T) {
	h := makeCFRTestHarness(12)
	ps := h.ep.currentPrefs()
	qf := func(cmd string) CommandStatus {
		t.Helper()
		status, err, _ := h.ep.tryExecuteUserCommand(h.ctx, cmd, nil, nil)
		if err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
		return status
	}

	if status := qf("QF AAL1"); len(status.responseArea) == 0 || len(h.ep.CFREntries) != 0 {
		t.Errorf("QF with the CFR view suppressed: RA %q, CFR %v", status.responseArea, h.ep.CFREntries)
	}
	ps.CFR.Visible = true
	if status := qf("QF AAL1"); len(status.responseArea) != 0 || !slices.Equal(h.ep.CFREntries, []sim.ACID{"AAL1"}) {
		t.Errorf("QF with the CFR view displayed: RA %q, CFR %v", status.responseArea, h.ep.CFREntries)
	}
	if status := qf("QF R AAL2"); len(status.responseArea) == 0 || len(h.ep.CFREntries) != 1 {
		t.Errorf("QF R: RA %q, CFR %v", status.responseArea, h.ep.CFREntries)
	}

	for i := 2; i <= 12; i++ {
		qf(fmt.Sprintf("QF AAL%d", i))
	}
	if len(h.ep.CFREntries) != 10 || h.ep.CFREntries[0] != "AAL3" {
		t.Errorf("after 12 readouts the view has %v, want AAL3 through AAL12", h.ep.CFREntries)
	}
	ps.CFR.Entries = 2
	h.ep.trimCFREntries()
	if !slices.Equal(h.ep.CFREntries, []sim.ACID{"AAL11", "AAL12"}) {
		t.Errorf("with ENTRIES 2 the view has %v, want the newest two", h.ep.CFREntries)
	}

	delete(h.ctx.Client.State.Tracks, "AAL11")
	h.frame(0)
	if !slices.Equal(h.ep.CFREntries, []sim.ACID{"AAL12"}) {
		t.Errorf("after AAL11 left the view has %v", h.ep.CFREntries)
	}
}

// The FILTERS toggles suppress columns, routes past RTE characters are cut
// short, and SHOW FIELDS shows everything (4.2.3).
func TestCFRLayout(t *testing.T) {
	h := makeCFRTestHarness(1)
	ps := h.ep.currentPrefs()
	h.ep.CFREntries = []sim.ACID{"AAL1"}

	l := h.ep.layoutCFR(h.ctx)
	e := l.entries[0]
	line := l.line("  "+e.flid, e.columns, e.route)
	if want := "  001 AAL1(15) B738/L 2301 450 SEY E1200 350 KBOS..SSOXS..SEY.PARCH4.KJFK"; line != want {
		t.Errorf("entry %q, want %q", line, want)
	}
	labels := l.labels(true)
	for _, c := range [][2]string{{"TYP", "B738"}, {"BCN", "2301"}, {"TIM", "E1200"}, {"RTE/RMK", "KBOS"}} {
		if strings.Index(labels, c[0]) != strings.Index(line, c[1]) {
			t.Errorf("label %s isn't over %s:\n%s\n%s", c[0], c[1], labels, line)
		}
	}

	ps.CFR.ShowCID, ps.CFR.ShowFix, ps.CFR.ShowTime = false, false, false
	ps.CFR.RouteChars = 30
	h.ctx.Client.State.Tracks["AAL1"].FlightPlan.Route = "SSOXS SEY HTO DPK PARCH4"
	l = h.ep.layoutCFR(h.ctx)
	e = l.entries[0]
	if got, want := l.line("  "+e.flid, e.columns, e.route), "  AAL1(15) B738/L 2301 450 350 KBOS..SSOXS..SEY..HTO..DPK.PAR"; got != want ||
		!e.truncated {
		t.Errorf("filtered entry %q truncated %v, want %q truncated", got, e.truncated, want)
	}

	h.ep.CFRShowFields = true
	l = h.ep.layoutCFR(h.ctx)
	if e = l.entries[0]; len(l.columns) != len(cfrColumns) || e.truncated || !strings.HasPrefix(e.flid, "001 ") {
		t.Errorf("SHOW FIELDS: %d columns, truncated %v, FLID %q", len(l.columns), e.truncated, e.flid)
	}
}

// eram/lists_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"slices"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// deptListNames returns the sublists' names and their flights' ACIDs.
func deptListNames(sublists []deptSublist) ([]string, [][]sim.ACID) {
	names := util.MapSlice(sublists, func(s deptSublist) string { return s.name })
	acids := util.MapSlice(sublists, func(s deptSublist) []sim.ACID {
		return util.MapSlice(s.flights, func(fp *sim.FlightPlan) sim.ACID { return fp.ACID })
	})
	return names, acids
}

// The Departure List view lists the proposed departures the user will
// work, in a sublist for each departure airport, in fix name order or
// one sorted by hand (4.2.12).
func TestDeptListSublists(t *testing.T) {
	h := makeTrackTestHarness()
	jfk, lga, phl, ewr := db.AirportDisplayId("KJFK"), db.AirportDisplayId("KLGA"), db.AirportDisplayId("KPHL"),
		db.AirportDisplayId("KEWR")
	departure := func(acid sim.ACID, airport av.ICAOAirportCode, tracking, inbound sim.ControlPosition) *sim.FlightPlan {
		return &sim.FlightPlan{ACID: acid, DepartureAirport: airport, TypeOfFlight: av.FlightTypeDeparture,
			TrackingController: tracking, InboundHandoffController: inbound, RequestedAltitude: 15000}
	}
	h.ctx.Client.State.UnassociatedFlightPlans = []*sim.FlightPlan{
		departure("SWA123", "KLGA", "1A", "1A"),
		departure("AAL321", "KJFK", "1A", "1A"),
		// A departure that a virtual controller hands off to the user.
		departure("UAL456", "KPHL", "T33", "1A"),
		departure("DAL1", "KJFK", "1A", "1A"),
		// Another sector's departure.
		departure("JBU2", "KJFK", "2B", "2B"),
		// Not departures.
		{ACID: "AAL9", DepartureAirport: "KJFK", TypeOfFlight: av.FlightTypeArrival, TrackingController: "1A"},
		{ACID: "AAL8", DepartureAirport: "KJFK", TypeOfFlight: av.FlightTypeDeparture, TrackingController: "1A",
			DeleteTime: sim.NewSimTime(h.start)},
	}

	names, acids := deptListNames(h.ep.deptSublists(h.ctx))
	if want := []string{jfk, lga, phl}; !slices.Equal(names, want) {
		t.Fatalf("sublists %v, want %v", names, want)
	}
	if want := []sim.ACID{"AAL321", "DAL1"}; !slices.Equal(acids[0], want) {
		t.Errorf("JFK flights %v, want %v", acids[0], want)
	}

	// TBPs on PHL and LGA and a TBE on LGA sort them to the top.
	p := &deptListMenu{}
	p.pickSublist(h.ep, h.ctx, phl, MenuClickPrimary)
	p.pickSublist(h.ep, h.ctx, lga, MenuClickTertiary)
	names, _ = deptListNames(h.ep.deptSublists(h.ctx))
	if want := []string{phl, lga, jfk}; !slices.Equal(names, want) {
		t.Errorf("sublists sorted by hand %v, want %v", names, want)
	}
	// A new sublist follows those sorted by hand.
	h.ctx.Client.State.UnassociatedFlightPlans = append(h.ctx.Client.State.UnassociatedFlightPlans,
		departure("N12A", "KEWR", "1A", "1A"))
	names, _ = deptListNames(h.ep.deptSublists(h.ctx))
	if want := []string{phl, lga, jfk, ewr}; !slices.Equal(names, want) {
		t.Errorf("sublists with a new one %v, want %v", names, want)
	}

	ps := h.ep.currentPrefs()
	ps.DeptList.SublistOrder = nil
	ps.DeptList.SuppressedSublists = []string{lga}
	names, _ = deptListNames(h.ep.displayedDeptSublists(h.ctx))
	if want := []string{ewr, jfk, phl}; !slices.Equal(names, want) {
		t.Errorf("displayed sublists in fix name order %v, want %v", names, want)
	}

	// DELETE and then a TBE on JFK deletes it and its flights.
	p.deleting = true
	p.pickSublist(h.ep, h.ctx, jfk, MenuClickTertiary)
	names, _ = deptListNames(h.ep.deptSublists(h.ctx))
	if want := []string{ewr, lga, phl}; !slices.Equal(names, want) || p.deleting {
		t.Errorf("sublists after deleting JFK %v, want %v; still deleting %v", names, want, p.deleting)
	}
	// A flight proposed later brings it back.
	h.ctx.Client.State.UnassociatedFlightPlans = append(h.ctx.Client.State.UnassociatedFlightPlans,
		departure("AAL5", "KJFK", "1A", "1A"))
	names, acids = deptListNames(h.ep.deptSublists(h.ctx))
	if i := slices.Index(names, jfk); i < 0 || !slices.Equal(acids[i], []sim.ACID{"AAL5"}) {
		t.Errorf("sublists %v %v, want JFK back with AAL5 alone", names, acids)
	}
}

func TestFormatFiledAltitude(t *testing.T) {
	for _, c := range []struct {
		fp   sim.FlightPlan
		want string
	}{
		{sim.FlightPlan{RequestedAltitude: 6000}, "60"},
		{sim.FlightPlan{RequestedAltitude: 15000}, "150"},
		{sim.FlightPlan{RequestedAltitude: 15000, AltitudeBlock: [2]int{17000, 21000}}, "170B210"},
		{sim.FlightPlan{Rules: av.FlightRulesVFR, RequestedAltitude: 5500}, "VFR"},
		{sim.FlightPlan{AssignedAltitudeQualifier: sim.OTPAltitude}, "OTP"},
	} {
		if got := formatFiledAltitude(&c.fp); got != c.want {
			t.Errorf("%+v: %q, want %q", c.fp, got, c.want)
		}
	}
}

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

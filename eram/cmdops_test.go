// eram/cmdops_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"slices"
	"strings"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/wx"

	"github.com/AllenDang/cimgui-go/imgui"
)

// QZ takes the VFR and VFR-on-top altitude forms.
func TestAltVFRParser(t *testing.T) {
	var p altVFRParser
	for _, tc := range []struct {
		text string
		want *altitudeEntry
	}{
		{"VFR AAL123", &altitudeEntry{qualifier: sim.VFRAltitude}},
		{"VFR/105 AAL123", &altitudeEntry{altitude: 10500, qualifier: sim.VFRAltitude}},
		{"OTP AAL123", &altitudeEntry{qualifier: sim.OTPAltitude}},
		{"OTP/105 AAL123", &altitudeEntry{altitude: 10500, qualifier: sim.OTPAltitude}},
		{"ABV/170 AAL123", nil},
		{"350 AAL123", nil},
	} {
		v, rest, ok, err := p.Parse(nil, nil, &CommandInput{}, tc.text)
		if tc.want == nil {
			if ok {
				t.Errorf("%q: parsed %v", tc.text, v)
			}
		} else if !ok || err != nil || v != *tc.want || rest != " AAL123" {
			t.Errorf("%q: got %v %q %v %v, want %v", tc.text, v, rest, ok, err, *tc.want)
		}
	}

	// The altitude has exactly three digits.
	for _, text := range []string{"VFR/95 AAL123", "OTP/1050 AAL123", "VFR/ AAL123"} {
		if _, _, ok, err := p.Parse(nil, nil, &CommandInput{}, text); !ok || err == nil {
			t.Errorf("%q: got ok %v, error %v; want a format error", text, ok, err)
		}
	}
}

// QF reads out the flight plan's aircraft data, with the number of aircraft
// and the heavy indicator, and its route from the departure airport to the
// destination. A flight not reporting ADS-B gets the non-ADS-B indicator
// (2.13).
func TestFlightPlanReadout(t *testing.T) {
	db.InitDB()
	InitCommands()
	ep := &Scope{
		prefSet:    &PreferenceSet{Current: *makeDefaultPreferences()},
		TrackState: map[av.ADSBCallsign]*TrackState{"AAL123": {}},
	}
	ctx := &scope.Context{Client: &client.ControlClient{}}
	ctx.Client.State.Tracks = map[av.ADSBCallsign]*sim.Track{
		"AAL123": {
			RadarTrack: av.RadarTrack{ADSBCallsign: "AAL123", Mode: av.TransponderModeAltitude},
			FlightPlan: &sim.FlightPlan{ACID: "AAL123", CID: "123", TrackingController: "15", AircraftType: "B744",
				AircraftCount: 2, EquipmentSuffix: "L", AssignedSquawk: 0o2301, CruiseSpeed: 480,
				AssignedAltitude: 35000, DepartureAirport: "KBOS", Route: "SSOXS SEY PARCH4", ArrivalAirport: "KJFK"},
		},
	}

	status, err, _ := ep.tryExecuteUserCommand(ctx, "QF AAL123", nil, nil)
	want := []string{"0000", "123 AAL123(15) 2H/B744/L 2301 480 350 KBOS..SSOXS..SEY.PARCH4.KJFK"}
	if err != nil || !slices.Equal(status.responseArea, want) {
		t.Errorf("QF AAL123: got %q, error %v; want %q", status.responseArea, err, want)
	}

	ctx.Client.State.Tracks["AAL123"].Mode = av.TransponderModeOn
	status, err, _ = ep.tryExecuteUserCommand(ctx, "QF AAL123", nil, nil)
	want[1] = nonADSBMark + " " + want[1]
	if err != nil || !slices.Equal(status.responseArea, want) {
		t.Errorf("QF AAL123 without Mode C: got %q, error %v; want %q", status.responseArea, err, want)
	}
}

// Readouts show routes in the NAS form: ".." between points flown direct,
// "." on either side of an airway or a procedure, and "./." after the
// departure airport of a route from present position (figures 2-32 and
// 4-253).
func TestFormatRoute(t *testing.T) {
	db.InitDB()
	ctx := &scope.Context{Client: &client.ControlClient{}}
	for _, tc := range []struct {
		dep, route, arr, want string
	}{
		{"KPHL", "PTW PTW320 SARAA J64 HLC J80 OAL MOD3", "KSFO", "KPHL..PTW.PTW320.SARAA.J64.HLC.J80.OAL.MOD3.KSFO"},
		{"KATL", "/. ROA205050 ROA HAR", "KPIT", "KATL./.ROA205050..ROA..HAR..KPIT"},
		{"KJFK", "DEEZZ5 CANDR J60 PSB", "KORD", "KJFK.DEEZZ5.CANDR.J60.PSB..KORD"},
		{"KORD", "./.CVG.V70.VHP", "KIND", "KORD./.CVG.V70.VHP..KIND"},
		{"KMSP", "CSHEL DCT KMSP", "KMSP", "KMSP..CSHEL..KMSP"},
	} {
		fp := sim.FlightPlan{DepartureAirport: av.ICAOAirportCode(tc.dep), Route: tc.route,
			ArrivalAirport: av.ICAOAirportCode(tc.arr)}
		if got := formatRoute(&fp, &ctx.Client.State); got != tc.want {
			t.Errorf("%q: got %s, want %s", tc.route, got, tc.want)
		}
	}
}

// Toggling the on-frequency indicator of a flight shown as an LDB expands it
// to an FDB, where the indicator is drawn.
func TestToggleVCIExpandsLDB(t *testing.T) {
	InitCommands()
	h := makeTrackTestHarness()
	trk := &sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "AAL123", Mode: av.TransponderModeAltitude,
			TransponderAltitude: 20000, Location: math.Point2LL{-73, 40}},
		FlightPlan: &sim.FlightPlan{ACID: "AAL123", CID: "123", TrackingController: "2B", OwningTCW: "TCW2"},
	}
	h.addTrack(trk)
	h.frame(0)
	if dt := h.ep.datablockType(h.ctx, *trk); dt != LimitedDatablock {
		t.Fatalf("got datablock type %v, want a limited datablock", dt)
	}

	if _, err, _ := h.ep.tryExecuteUserCommand(h.ctx, "//AAL123", nil, nil); err != nil {
		t.Fatalf("//AAL123: %v", err)
	}
	if dt := h.ep.datablockType(h.ctx, *trk); dt != FullDatablock || !h.ep.TrackState["AAL123"].DisplayVCI {
		t.Errorf("//AAL123: got datablock type %v and VCI %v, want a full datablock with the VCI", dt,
			h.ep.TrackState["AAL123"].DisplayVCI)
	}
}

// The on-frequency indicator is toggled by QN // and QZ //, and by // with
// QN implied (5.1.1.1, 2.15.1).
func TestToggleVCIPrefixes(t *testing.T) {
	InitCommands()
	h := makeTrackTestHarness()
	h.addTrack(&sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "AAL123"},
		FlightPlan: &sim.FlightPlan{ACID: "AAL123", CID: "123"},
	})
	h.frame(0)

	for i, cmd := range []string{"QN // AAL123", "QN //AAL123", "QZ // AAL123", "QZ //AAL123", "// AAL123"} {
		if _, err, _ := h.ep.tryExecuteUserCommand(h.ctx, cmd, nil, nil); err != nil {
			t.Errorf("%s: %v", cmd, err)
		}
		if got, want := h.ep.TrackState["AAL123"].DisplayVCI, i%2 == 0; got != want {
			t.Errorf("%s: got VCI %v, want %v", cmd, got, want)
		}
	}
}

// Handoffs and data block offsets may be entered with QN or QZ, and the
// implied command with QN (2.15.1, Appendix B).
func TestQNQZPrefixes(t *testing.T) {
	InitCommands()
	h := makeTrackTestHarness()
	h.addTrack(&sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "AAL123"},
		FlightPlan: &sim.FlightPlan{ACID: "AAL123", CID: "123", TrackingController: "1A", OwningTCW: "TCW1"},
	})
	h.addTrack(&sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "UAL456"},
		FlightPlan: &sim.FlightPlan{ACID: "UAL456", CID: "456", TrackingController: "2B", OwningTCW: "TCW2"},
	})
	h.frame(0)
	state := h.ep.TrackState["AAL123"]
	state.LeaderLineLength = new(1)

	for _, tc := range []struct {
		cmd       string
		direction math.CardinalOrdinalDirection
		length    int
	}{
		{"QZ 8 AAL123", math.North, 1},
		{"QN 2 /123", math.South, 1},
		{"QZ /3 /123", math.South, 3},
		{"QN 9/2 AAL123", math.NorthEast, 2},
	} {
		if _, err, _ := h.ep.tryExecuteUserCommand(h.ctx, tc.cmd, nil, nil); err != nil {
			t.Errorf("%s: %v", tc.cmd, err)
		} else if *state.LeaderLineDirection != tc.direction || *state.LeaderLineLength != tc.length {
			t.Errorf("%s: got direction %v length %d, want %v %d", tc.cmd, *state.LeaderLineDirection,
				*state.LeaderLineLength, tc.direction, tc.length)
		}
	}

	// A handoff to a sector that is not signed on is rejected by the
	// handoff command.
	for _, cmd := range []string{"QZ 9Z AAL123", "QN 9Z /123"} {
		if _, err, _ := h.ep.tryExecuteUserCommand(h.ctx, cmd, nil, nil); err != ErrSectorNotActive {
			t.Errorf("%s: got error %v, want %v", cmd, err, ErrSectorNotActive)
		}
	}

	if _, err, _ := h.ep.tryExecuteUserCommand(h.ctx, "QN UAL456", nil, nil); err != nil || !h.ep.TrackState["UAL456"].EFDB {
		t.Errorf("QN UAL456: got FDB %v, error %v; want an FDB", h.ep.TrackState["UAL456"].EFDB, err)
	}
}

// QX is refused for another sector's track, naming the sector that has
// control.
func TestDropTrackOfAnotherSector(t *testing.T) {
	InitCommands()
	h := makeTrackTestHarness()
	h.ctx.Client.State.Controllers = map[sim.ControlPosition]*av.Controller{"2B": {Position: "2B"}}
	h.addTrack(&sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "AAL123"},
		FlightPlan: &sim.FlightPlan{ACID: "AAL123", CID: "123", TrackingController: "2B"},
	})
	h.frame(0)

	_, err, _ := h.ep.tryExecuteUserCommand(h.ctx, "QX AAL123", nil, nil)
	if want := "SECTOR 2B HAS CONTROL\nDROP TRACK AAL123"; err == nil || err.Error() != want {
		t.Errorf("QX AAL123: got error %v, want %q", err, want)
	}
}

// LB reports its function as RANGE/BEARING/FIX, which distinguishes it from
// LA's RANGE/BEARING (3.12.2, figure 3-29).
func TestRangeBearingFixFeedback(t *testing.T) {
	InitCommands()
	h := makeTrackTestHarness()
	h.ctx.Client.State.Fixes = map[string]math.Point2LL{"ABC": {-72, 40}}
	h.addTrack(&sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "AAL123", Location: math.Point2LL{-73, 40}, Groundspeed: 300},
		FlightPlan: &sim.FlightPlan{ACID: "AAL123", CID: "123"},
	})
	h.frame(0)

	for _, cmd := range []string{"LB ABC AAL123", "LB ABC/250 AAL123"} {
		status, err, _ := h.ep.tryExecuteUserCommand(h.ctx, cmd, nil, nil)
		if want := []string{"ACCEPT", "RANGE/BEARING/FIX"}; err != nil || !slices.Equal(status.feedbackArea, want) {
			t.Errorf("%s: got %q, error %v; want %q", cmd, status.feedbackArea, err, want)
		}
	}
}

// A CRR group may be located at an airport given by its FAA identifier,
// which also labels the group (4.2.11.4.1).
func TestCRRAtAirport(t *testing.T) {
	db.InitDB()
	InitCommands()
	h := makeTrackTestHarness()

	if _, err, _ := h.ep.tryExecuteUserCommand(h.ctx, "LF //IAD", nil, nil); err != nil {
		t.Fatalf("LF //IAD: %v", err)
	}
	g, ok := h.ep.CRRGroups["IAD"]
	if !ok {
		t.Fatalf("LF //IAD: no IAD group in %v", h.ep.CRRGroups)
	}
	if want := db.DB.Airports["KIAD"].Location; g.Location != want {
		t.Errorf("LF //IAD: group at %v; want %v", g.Location, want)
	}
}

// LF with a picked point and a label creates a group there, with the
// flights that follow; it rejects the label of an existing group rather
// than adding a picked flight to the group (4.2.11.4.1, 4.2.11.4.2).
func TestCRRAtPoint(t *testing.T) {
	InitCommands()
	h := makeTrackTestHarness()
	trackAt := math.Point2LL{-73, 40}
	h.addTrack(&sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "AAL1", Location: trackAt},
		FlightPlan: &sim.FlightPlan{ACID: "AAL1"},
	})
	point := math.Point2LL{-73.1, 40.1}
	lf := func(cmd string, clicks []math.Point2LL, tracks []av.ADSBCallsign) error {
		_, err, _ := h.ep.tryExecuteUserCommand(h.ctx, cmd, clicks, tracks)
		return err
	}

	if err := lf("LF w ABC", []math.Point2LL{point}, []av.ADSBCallsign{""}); err != nil {
		t.Fatalf("LF {point} ABC: %v", err)
	}
	if g := h.ep.CRRGroups["ABC"]; g == nil || g.Location != point || len(g.Aircraft) != 0 {
		t.Fatalf("LF {point} ABC: group %+v", g)
	}

	err := lf("LF w ABC", []math.Point2LL{trackAt}, []av.ADSBCallsign{"AAL1"})
	if err == nil || !strings.Contains(err.Error(), "ALREADY EXISTS") {
		t.Errorf("LF {AAL1} ABC with ABC existing: error %v", err)
	}
	if g := h.ep.CRRGroups["ABC"]; len(g.Aircraft) != 0 {
		t.Errorf("LF {AAL1} ABC with ABC existing: aircraft %v", g.Aircraft)
	}

	if err := lf("LF w DEF w", []math.Point2LL{point, trackAt}, []av.ADSBCallsign{"", "AAL1"}); err != nil {
		t.Fatalf("LF {point} DEF {AAL1}: %v", err)
	}
	if g := h.ep.CRRGroups["DEF"]; g == nil || g.Location != point || len(g.Aircraft) != 1 {
		t.Errorf("LF {point} DEF {AAL1}: group %+v", g)
	}
}

// LC reads out the change to the ground speed that puts the aircraft over
// the fix at the given time, and its present ground speed (3.12.3, figure
// 3-30).
func TestFixTimeReadout(t *testing.T) {
	InitCommands()
	h := makeTrackTestHarness()
	h.ctx.Client.State.Fixes = map[string]math.Point2LL{"ABC": {-73, 41}}
	h.addTrack(&sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "AAL123", Location: math.Point2LL{-73, 40}, Groundspeed: 300},
		FlightPlan: &sim.FlightPlan{ACID: "AAL123", CID: "123"},
	})
	h.frame(0)

	// 60 nm to go in 15 minutes takes 240 knots.
	for _, tc := range []struct {
		cmd  string
		want []string
	}{
		{"LC ABC/1215 AAL123", []string{"ABC AT 1215Z-60K", "PRESENT SPEED 300"}},
		{"LC ABC/1210 AAL123", []string{"ABC AT 1210Z+60K", "PRESENT SPEED 300"}},
	} {
		status, err, _ := h.ep.tryExecuteUserCommand(h.ctx, tc.cmd, nil, nil)
		if err != nil || !slices.Equal(status.responseArea, tc.want) ||
			!slices.Equal(status.feedbackArea, []string{"ACCEPT", "FIX/TIME"}) {
			t.Errorf("%s: got %q %q, error %v; want %q", tc.cmd, status.feedbackArea, status.responseArea, err, tc.want)
		}
	}
}

// LD reads out the time at each fix of the route through the given one,
// and LE the change to the ground speed that reaches the fix at the given
// time; both fly the route from the track's position (3.12.4, 3.12.5).
func TestRouteFixTimeReadouts(t *testing.T) {
	InitCommands()
	h := makeTrackTestHarness()
	h.ctx.Client.State.Fixes = map[string]math.Point2LL{"ABC": {-73, 41}, "DEF": {-73, 40.5}, "GHI": {-73, 42}}
	h.addTrack(&sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "AAL123", Location: math.Point2LL{-73, 40}, Groundspeed: 300},
		FlightPlan: &sim.FlightPlan{ACID: "AAL123", CID: "123"},
		RouteFixes: []string{"ABC", "DEF", "GHI"},
	})
	h.frame(0)

	// The route doubles back: ABC is 60 nm north and DEF 30 nm back south
	// of it, 12 and 18 minutes away at 300 knots. The 90 nm to DEF in 30
	// minutes take 180 knots.
	for _, tc := range []struct {
		cmd      string
		feedback string
		want     []string
	}{
		{"LD DEF AAL123", "RTE FIX/TME RO", []string{"ABC AT 1212Z", "DEF AT 1218Z"}},
		{"LE DEF/1230 AAL123", "RTE SPEED ADJ", []string{"DEF AT 1230Z-120K", "PRESENT SPEED 300"}},
	} {
		status, err, _ := h.ep.tryExecuteUserCommand(h.ctx, tc.cmd, nil, nil)
		if err != nil || !slices.Equal(status.responseArea, tc.want) ||
			!slices.Equal(status.feedbackArea, []string{"ACCEPT", tc.feedback}) {
			t.Errorf("%s: got %q %q, error %v; want %q %q", tc.cmd, status.feedbackArea, status.responseArea, err,
				tc.feedback, tc.want)
		}
	}

	for _, cmd := range []string{"LD XYZ AAL123", "LE XYZ/1230 AAL123"} {
		if _, err, _ := h.ep.tryExecuteUserCommand(h.ctx, cmd, nil, nil); err != ErrIllegalValue {
			t.Errorf("%s: got error %v, want %v for a fix not on the route", cmd, err, ErrIllegalValue)
		}
	}
}

// QB adds a code to the CODE view or removes it, and says so (4.2.4.2,
// figure 4-31).
func TestBeaconCodeViewFeedback(t *testing.T) {
	InitCommands()
	h := makeTrackTestHarness()
	for _, want := range [][]av.Squawk{{0o1200}, nil} {
		status, err, _ := h.ep.tryExecuteUserCommand(h.ctx, "QB 1200", nil, nil)
		if err != nil || !slices.Equal(status.feedbackArea, []string{"ACCEPT", "BC LIST UPDATE"}) ||
			!slices.Equal(h.ep.AddedBeaconCodes, want) {
			t.Errorf("QB 1200: got %q and codes %v, error %v; want ACCEPT / BC LIST UPDATE and %v",
				status.feedbackArea, h.ep.AddedBeaconCodes, err, want)
		}
	}
}

// QU displays a route for 1 to 99 minutes (2.11), which may be given before
// or after the FLID.
func TestRouteDisplayMinutesRange(t *testing.T) {
	InitCommands()
	h := makeTrackTestHarness()
	h.addTrack(&sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "AAL123"},
		FlightPlan: &sim.FlightPlan{ACID: "AAL123", CID: "123"},
	})
	h.frame(0)

	for _, cmd := range []string{"QU 0 AAL123", "QU 100 AAL123", "QU 500 123", "QU AAL123 0", "QU 123 100"} {
		if _, err, _ := h.ep.tryExecuteUserCommand(h.ctx, cmd, nil, nil); err != ErrIllegalValue {
			t.Errorf("%s: got error %v, want %v", cmd, err, ErrIllegalValue)
		}
	}
}

// AR and WR take station identifiers of two to five characters (4.2.1.1.2,
// 4.2.20.1); those that are not in the database are unknown rather than
// invalid.
func TestStationIDLength(t *testing.T) {
	db.InitDB()
	InitCommands()
	h := makeTrackTestHarness()
	for _, tc := range []struct {
		cmd, want string
	}{
		{"AR Q", "REJECT - AR - INVALID AIRPORT CODE"},
		{"AR QQ", "REJECT - AR - UNKNOWN AIRPORT"},
		{"AR QQQQQ", "REJECT - AR - UNKNOWN AIRPORT"},
		{"AR QQQQQQ", "REJECT - AR - INVALID AIRPORT CODE"},
		{"WR QQQQQ", "REJECT - WR - UNKNOWN AIRPORT"},
		{"WR R QQ", "REJECT - WR R - UNKNOWN AIRPORT"},
	} {
		if _, err, _ := h.ep.tryExecuteUserCommand(h.ctx, tc.cmd, nil, nil); err == nil || err.Error() != tc.want {
			t.Errorf("%s: got error %v, want %s", tc.cmd, err, tc.want)
		}
	}
}

// WR adds up to three stations at once, or removes them (4.2.20.2).
func TestWXReportStations(t *testing.T) {
	db.InitDB()
	InitCommands()
	h := makeTrackTestHarness()
	h.ctx.Client.State.METAR = map[av.ICAOAirportCode]wx.METAR{"KJFK": {}, "KBOS": {}, "KEWR": {}}

	for _, tc := range []struct {
		cmd  string
		err  error
		want []av.ICAOAirportCode
	}{
		{"WR KJFK BOS KEWR", nil, []av.ICAOAirportCode{"KJFK", "KBOS", "KEWR"}},
		{"WR KBOS", nil, []av.ICAOAirportCode{"KJFK", "KEWR"}},
		{"WR KJFK QQQQQ", NewError("REJECT - WR - UNKNOWN AIRPORT"), []av.ICAOAirportCode{"KJFK", "KEWR"}},
		{"WR KJFK KBOS KEWR KJFK", ErrMessageTooLong, []av.ICAOAirportCode{"KJFK", "KEWR"}},
	} {
		_, err, _ := h.ep.tryExecuteUserCommand(h.ctx, tc.cmd, nil, nil)
		if (err == nil) != (tc.err == nil) || (err != nil && err.Error() != tc.err.Error()) ||
			!slices.Equal(h.ep.WXReportStations, tc.want) {
			t.Errorf("%s: got %v, error %v; want %v, error %v", tc.cmd, h.ep.WXReportStations, err, tc.want, tc.err)
		}
	}
}

// AR puts a station in the first empty position of the ALTIM SET view and
// removes one by emptying its position (4.2.1.1.2).
func TestAltimSetStations(t *testing.T) {
	db.InitDB()
	InitCommands()
	h := makeTrackTestHarness()
	h.ctx.Client.State.METAR = map[av.ICAOAirportCode]wx.METAR{"KJFK": {}, "KBOS": {}, "KEWR": {}, "KLGA": {}}

	for _, tc := range []struct {
		cmd  string
		want []av.ICAOAirportCode
	}{
		{"AR KJFK", []av.ICAOAirportCode{"KJFK"}},
		{"AR BOS", []av.ICAOAirportCode{"KJFK", "KBOS"}},
		{"AR KEWR", []av.ICAOAirportCode{"KJFK", "KBOS", "KEWR"}},
		{"AR KJFK", []av.ICAOAirportCode{"", "KBOS", "KEWR"}},
		{"AR KEWR", []av.ICAOAirportCode{"", "KBOS"}},
		{"AR KLGA", []av.ICAOAirportCode{"KLGA", "KBOS"}},
		{"AR KBOS", []av.ICAOAirportCode{"KLGA"}},
	} {
		if _, err, _ := h.ep.tryExecuteUserCommand(h.ctx, tc.cmd, nil, nil); err != nil ||
			!slices.Equal(h.ep.AltimSetAirports, tc.want) {
			t.Errorf("%s: got %q, error %v; want %q", tc.cmd, h.ep.AltimSetAirports, err, tc.want)
		}
	}
}

// The ALTIM SET view gives the observation time in UTC, whatever zone the
// METAR's time was decoded in.
func TestAltimSetTimeIsUTC(t *testing.T) {
	obs := time.Date(2026, 10, 20, 22, 37, 0, 0, time.UTC)
	metar := wx.METAR{Time: obs.In(time.FixedZone("EDT", -4*60*60))}
	if ts, _, _ := altimMetarForDisplay(metar); ts != "2237" {
		t.Errorf("got time %q; want %q", ts, "2237")
	}
}

type fakeClipboard struct{ text string }

func (c *fakeClipboard) GetClipboard() string  { return c.text }
func (c *fakeClipboard) SetClipboard(s string) { c.text = s }

// clipboardPlatform stands in for the platform, whose clipboard .DRAWROUTE
// copies the route's points to.
type clipboardPlatform struct {
	platform.Platform
	clipboard fakeClipboard
}

func (p *clipboardPlatform) GetClipboard() imgui.ClipboardHandler { return &p.clipboard }

// .DRAWROUTE takes clicks as the points of a route, which it copies to the
// clipboard, until Escape; Backspace removes the last point.
func TestDrawRoute(t *testing.T) {
	InitCommands()
	ep := &Scope{prefSet: &PreferenceSet{Current: *makeDefaultPreferences()}}
	pl := &clipboardPlatform{}
	ctx := &scope.Context{DrawExtent: math.Extent2D{P1: [2]float32{1600, 900}}, Platform: pl,
		Client: &client.ControlClient{}}
	transforms := scope.GetTransformations(ctx.DrawExtent, 45, math.Point2LL{-77, 39}, 100, 0)
	click := func(p [2]float32) {
		ctx.Mouse, ctx.Keyboard = &platform.MouseState{Pos: p}, nil
		ctx.Mouse.Clicked[platform.MouseButtonPrimary] = true
		ep.consumeMouseEvents(ctx, transforms)
	}
	press := func(k imgui.Key) {
		ctx.Mouse, ctx.Keyboard = nil, &platform.KeyboardState{Pressed: map[imgui.Key]any{k: nil}}
		ep.processKeyboardInput(ctx)
	}

	status, err, _ := ep.tryExecuteUserCommand(ctx, ".DRAWROUTE", nil, nil)
	ep.applyCommandStatus(ctx, status, err)
	click([2]float32{800, 450})
	click([2]float32{900, 500})
	if n := len(strings.Fields(pl.clipboard.text)); len(ep.drawRoutePoints) != 2 || n != 2 ||
		ep.responseArea != "DRAWROUTE: 2 POINTS" {
		t.Fatalf("after two clicks: %d points, %d on the clipboard, RA %q", len(ep.drawRoutePoints), n, ep.responseArea)
	}

	press(imgui.KeyBackspace)
	if n := len(strings.Fields(pl.clipboard.text)); len(ep.drawRoutePoints) != 1 || n != 1 {
		t.Errorf("after Backspace: %d points, %d on the clipboard", len(ep.drawRoutePoints), n)
	}

	press(imgui.KeyEscape)
	click([2]float32{800, 450})
	if ep.drawingRoute || len(ep.drawRoutePoints) != 0 || ep.responseArea != "" {
		t.Errorf("after Escape and a click: drawing %v, %d points, RA %q", ep.drawingRoute, len(ep.drawRoutePoints),
			ep.responseArea)
	}
}

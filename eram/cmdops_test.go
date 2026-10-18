// eram/cmdops_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"slices"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
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
// destination.
func TestFlightPlanReadout(t *testing.T) {
	db.InitDB()
	InitCommands()
	ep := &Scope{
		prefSet:    &PrefrenceSet{Current: *makeDefaultPreferences()},
		TrackState: map[av.ADSBCallsign]*TrackState{"AAL123": {}},
	}
	ctx := &scope.Context{Client: &client.ControlClient{}}
	ctx.Client.State.Tracks = map[av.ADSBCallsign]*sim.Track{
		"AAL123": {
			RadarTrack: av.RadarTrack{ADSBCallsign: "AAL123"},
			FlightPlan: &sim.FlightPlan{ACID: "AAL123", CID: "123", TrackingController: "15", AircraftType: "B744",
				AircraftCount: 2, EquipmentSuffix: "L", AssignedSquawk: 0o2301, CruiseSpeed: 480,
				AssignedAltitude: 35000, DepartureAirport: "KBOS", Route: "SSOXS SEY PARCH4", ArrivalAirport: "KJFK"},
		},
	}

	status, err, _ := ep.tryExecuteUserCommand(ctx, "QF AAL123", nil, nil)
	want := []string{"0000", "123 AAL123(15) 2H/B744/L 2301 480 350 KBOS.SSOXS.SEY.PARCH4.KJFK"}
	if err != nil || !slices.Equal(status.responseArea, want) {
		t.Errorf("QF AAL123: got %q, error %v; want %q", status.responseArea, err, want)
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

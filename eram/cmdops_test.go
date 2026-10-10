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

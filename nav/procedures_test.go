// nav/procedures_test.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"reflect"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/wx"
)

func TestManeuverCompleteUntilAltitude(t *testing.T) {
	nav := &Nav{FlightState: FlightState{Altitude: 499}}
	mc := ManeuverComplete{Type: UntilAltitude, Altitude: 500}

	if mc.Done(nav, Time{}, wx.Sample{}, 0) {
		t.Fatal("expected altitude completion to wait below target altitude")
	}

	nav.FlightState.Altitude = 500
	if !mc.Done(nav, Time{}, wx.Sample{}, 0) {
		t.Fatal("expected altitude completion at target altitude")
	}
}

func TestManeuverCompleteUntilDME(t *testing.T) {
	const nmPerLongitude = 45
	dmeFix := math.Point2LL{-74, 40}
	start := math.Offset2LL(dmeFix, 90, 3, nmPerLongitude)
	crossed := math.Offset2LL(dmeFix, 90, 4.1, nmPerLongitude)

	nav := &Nav{FlightState: FlightState{
		Position: start,
		Altitude: 3000,
	}}
	mc := ManeuverComplete{
		Type:            UntilDME,
		DMEDistance:     4,
		DMEFix:          dmeFix,
		DMEFixElevation: 33,
	}

	if mc.Done(nav, Time{}, wx.Sample{}, 0) {
		t.Fatal("expected DME completion to wait below target distance")
	}

	nav.FlightState.Position = crossed
	if !mc.Done(nav, Time{}, wx.Sample{}, 0) {
		t.Fatal("expected DME completion at target slant distance")
	}
}

func TestRacetrackEntryManeuvers(t *testing.T) {
	fix := math.Point2LL{-75.109550, 40.880634}
	entryLeg := func(track math.MagneticHeading) LateralManeuver {
		return flyTrackForTime(track, 70)
	}

	for _, tc := range []struct {
		name    string
		entry   av.HoldEntry
		inbound math.MagneticHeading
		turn    av.TurnDirection
		want    []LateralManeuver
	}{
		{
			name:    "direct",
			entry:   av.HoldEntryDirect,
			inbound: 90,
			turn:    av.TurnRight,
			want: []LateralManeuver{
				flyTowardFix(fix),
			},
		},
		{
			name:    "right parallel",
			entry:   av.HoldEntryParallel,
			inbound: 90,
			turn:    av.TurnRight,
			want: []LateralManeuver{
				flyTowardFix(fix),
				turnToTrack(270, av.TurnClosest),
				flyTrackForTime(270, 70),
				turnToTrack(50, av.TurnLeft),
				flyTrackUntilIntercept(50, av.TurnRight, fix, 90),
				turnToTrack(90, av.TurnRight),
				flyTowardFix(fix),
			},
		},
		{
			name:    "right teardrop",
			entry:   av.HoldEntryTeardrop,
			inbound: 90,
			turn:    av.TurnRight,
			want: []LateralManeuver{
				flyTowardFix(fix),
				turnToTrack(240, av.TurnClosest),
				flyTrackForTime(240, 70),
				turnToTrack(60, av.TurnRight),
				flyTrackUntilIntercept(60, av.TurnRight, fix, 90),
				turnToTrack(90, av.TurnRight),
				flyTowardFix(fix),
			},
		},
		{
			name:    "left parallel",
			entry:   av.HoldEntryParallel,
			inbound: 90,
			turn:    av.TurnLeft,
			want: []LateralManeuver{
				flyTowardFix(fix),
				turnToTrack(270, av.TurnClosest),
				flyTrackForTime(270, 70),
				turnToTrack(130, av.TurnRight),
				flyTrackUntilIntercept(130, av.TurnLeft, fix, 90),
				turnToTrack(90, av.TurnLeft),
				flyTowardFix(fix),
			},
		},
		{
			name:    "left teardrop",
			entry:   av.HoldEntryTeardrop,
			inbound: 90,
			turn:    av.TurnLeft,
			want: []LateralManeuver{
				flyTowardFix(fix),
				turnToTrack(300, av.TurnClosest),
				flyTrackForTime(300, 70),
				turnToTrack(120, av.TurnLeft),
				flyTrackUntilIntercept(120, av.TurnLeft, fix, 90),
				turnToTrack(90, av.TurnLeft),
				flyTowardFix(fix),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := racetrackEntryManeuvers(tc.entry, fix, tc.inbound, tc.turn, entryLeg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("unexpected maneuvers:\ngot:  %#v\nwant: %#v", got, tc.want)
			}
		})
	}
}

func TestManeuverCompleteUntilRadial(t *testing.T) {
	const nmPerLongitude = 45
	fix := math.Point2LL{-74, 40}
	nav := &Nav{FlightState: FlightState{NmPerLongitude: nmPerLongitude}}

	// With no magnetic variation the 090 radial runs due east from the fix.
	at := func(bearing math.TrueHeading) {
		nav.FlightState.Position = math.Offset2LL(fix, bearing, 3, nmPerLongitude)
	}
	mc := ManeuverComplete{Type: UntilRadial, Fix: fix, Radial: 90}

	at(45) // north of the radial
	if mc.Done(nav, Time{}, wx.Sample{}, 0) {
		t.Fatal("expected radial completion to wait before crossing")
	}
	at(135) // south of it, east of the fix
	if !mc.Done(nav, Time{}, wx.Sample{}, 0) {
		t.Fatal("expected radial completion after crossing the radial")
	}

	// The reciprocal is the same line, but it isn't the radial.
	mc = ManeuverComplete{Type: UntilRadial, Fix: fix, Radial: 90}
	at(315)
	if mc.Done(nav, Time{}, wx.Sample{}, 0) {
		t.Fatal("expected radial completion to wait before crossing")
	}
	at(225)
	if mc.Done(nav, Time{}, wx.Sample{}, 0) {
		t.Fatal("crossing the reciprocal of the radial shouldn't count")
	}
	at(135)
	if !mc.Done(nav, Time{}, wx.Sample{}, 0) {
		t.Fatal("expected radial completion after crossing the radial")
	}
}

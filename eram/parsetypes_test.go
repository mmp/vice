// eram/parsetypes_test.go
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

// TestFixesAndTrackParser checks what QU amends a route with: fixes or
// locations, then the track, given by its FLID or clicked. A number there is
// the count of minutes QU displays a route for, not a fix.
func TestFixesAndTrackParser(t *testing.T) {
	db.InitDB()
	ctx := &scope.Context{Client: &client.ControlClient{}}
	ctx.Client.State.Fixes = map[string]math.Point2LL{"ALPHA": {-73, 40}}
	ctx.Client.State.Tracks = map[av.ADSBCallsign]*sim.Track{
		"AAL123": {
			RadarTrack: av.RadarTrack{ADSBCallsign: "AAL123"},
			FlightPlan: &sim.NASFlightPlan{ACID: "AAL123", CID: "123"},
		},
	}

	clicked := &CommandInput{mousePositions: []math.Point2LL{{-73, 40}}, trackCallsigns: []av.ADSBCallsign{"AAL123"}}

	var p fixesAndTrackParser
	for _, tc := range []struct {
		text  string
		input *CommandInput
		fixes []string // nil if the text isn't a route amendment
	}{
		{"GDM BOSOX 123", &CommandInput{}, []string{"GDM", "BOSOX"}},
		{"ALPHA090030 BOSOX 123", &CommandInput{}, []string{"ALPHA090030", "BOSOX"}},
		{"GDM " + locationSymbol, clicked, []string{"GDM"}},
		{"20 " + locationSymbol, clicked, nil},
		{"BOSOX 999", &CommandInput{}, nil},
		{"BOSOX", &CommandInput{}, nil},
	} {
		v, _, ok, err := p.Parse(nil, ctx, tc.input, tc.text)
		if err != nil {
			t.Errorf("%q: %v", tc.text, err)
			continue
		}
		if ok != (tc.fixes != nil) {
			t.Errorf("%q: parsed %v, want %v", tc.text, ok, tc.fixes != nil)
			continue
		}
		if !ok {
			continue
		}
		ft := v.(fixesAndTrack)
		if !slices.Equal(ft.fixes, tc.fixes) || ft.trk == nil || ft.trk.ADSBCallsign != "AAL123" {
			t.Errorf("%q: got fixes %v for %v, want %v for AAL123", tc.text, ft.fixes, ft.trk, tc.fixes)
		}
	}
}

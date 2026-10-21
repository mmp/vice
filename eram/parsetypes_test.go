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
			FlightPlan: &sim.FlightPlan{ACID: "AAL123", CID: "123"},
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

// Free text has one form whether a scenario entered it or the controller
// typed it, so that heading and speed entries recognize it to replace it.
func TestQSFreeText(t *testing.T) {
	var p hsfTextParser
	v, rest, ok, err := p.Parse(nil, nil, &CommandInput{}, formatInput("`EXPEDITE AAL123"))
	if !ok || err != nil || v != "EXPEDITE" || rest != " AAL123" {
		t.Errorf("`EXPEDITE AAL123: got %v %q %v %v, want EXPEDITE", v, rest, ok, err)
	}

	for _, c := range []struct {
		scratchpad, text string
	}{
		{sim.QSFreeTextIndicator + "EXPEDITE", "EXPEDITE"},
		{"250", ""},
		{"", ""},
	} {
		if isQSFreeText(c.scratchpad) != (c.text != "") || stripQSFreeTextIndicator(c.scratchpad) != c.text {
			t.Errorf("%q: got free text %v %q, want %q", c.scratchpad, isQSFreeText(c.scratchpad),
				stripQSFreeTextIndicator(c.scratchpad), c.text)
		}
	}
}

// A FLID may be preceded by a slash, as Appendix B shows for every command.
func TestTrackParserSlashedFLID(t *testing.T) {
	ctx := &scope.Context{Client: &client.ControlClient{}}
	ctx.Client.State.Tracks = map[av.ADSBCallsign]*sim.Track{
		"AAL123": {
			RadarTrack: av.RadarTrack{ADSBCallsign: "AAL123"},
			FlightPlan: &sim.FlightPlan{ACID: "AAL123", CID: "123", AssignedSquawk: 0o2301},
		},
	}

	var p trackParser
	for _, tc := range []struct {
		text string
		ok   bool
	}{
		{"/123", true},
		{"/AAL123", true},
		{"123", true},
		{"//123", false},
		{"/124", false},
	} {
		v, _, ok, err := p.Parse(nil, ctx, &CommandInput{}, tc.text)
		if err != nil || ok != tc.ok || (ok && v.(*sim.Track).ADSBCallsign != "AAL123") {
			t.Errorf("%q: got %v %v %v, want matched %v", tc.text, v, ok, err, tc.ok)
		}
	}

	InitCommands()
	ep := &Scope{prefSet: &PrefrenceSet{Current: *makeDefaultPreferences()},
		TrackState: map[av.ADSBCallsign]*TrackState{"AAL123": {}}}
	if _, err, _ := ep.tryExecuteUserCommand(ctx, "QP J /123", nil, nil); err != nil || !ep.TrackState["AAL123"].DisplayJRing {
		t.Errorf("QP J /123: got J ring %v, error %v", ep.TrackState["AAL123"].DisplayJRing, err)
	}
}

// QS takes a Mach speed with or without its decimal point (11.1.1.1), and a
// heading and a speed together, as in QS 330/81 (Appendix B).
func TestQSHeadingAndSpeed(t *testing.T) {
	var sp hsfSpeedParser
	for _, text := range []string{"/M.61 AAL123", "/M61 AAL123"} {
		if v, rest, ok, err := sp.Parse(nil, nil, &CommandInput{}, text); !ok || err != nil || v != "M61" || rest != " AAL123" {
			t.Errorf("%q: got %v %q %v %v, want M61", text, v, rest, ok, err)
		}
	}

	// Speeds are stored in the form of the speed commands, which take + and
	// -; Mach has two digits.
	for _, tc := range []struct{ text, want string }{
		{"/250+ AAL123", "S250+"},
		{"/250- AAL123", "S250-"},
		{"/78+ AAL123", "M78+"},
		{"/M78- AAL123", "M78-"},
		{"/M750 AAL123", ""},
		{"/M.750 AAL123", ""},
	} {
		v, _, ok, err := sp.Parse(nil, nil, &CommandInput{}, tc.text)
		if tc.want == "" {
			if !ok || err == nil {
				t.Errorf("%q: got %v %v %v, want an error", tc.text, v, ok, err)
			}
		} else if !ok || err != nil || v != tc.want {
			t.Errorf("%q: got %v %v %v, want %s", tc.text, v, ok, err, tc.want)
		}
	}

	var hp hsfHeadingParser
	for _, tc := range []struct {
		text, heading, rest string
	}{
		{"330/81 AAL123", "330", "/81 AAL123"},
		{"330 AAL123", "330", " AAL123"},
		{"/330 AAL123", "", ""},
		{"*/ AAL123", "", ""},
	} {
		v, rest, ok, err := hp.Parse(nil, nil, &CommandInput{}, tc.text)
		if err != nil || ok != (tc.heading != "") || (ok && (v != tc.heading || rest != tc.rest)) {
			t.Errorf("%q: got %v %q %v %v, want %q %q", tc.text, v, rest, ok, err, tc.heading, tc.rest)
		}
	}
}

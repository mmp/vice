// navtrace/navtrace_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package navtrace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/wx"
)

func TestWriteAndLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "traces")
	s, err := NewSession(dir, "N90", "KJFK Depart 4L Land 4L/R", "abc123")
	if err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 10, 6, 2, 30, 0, 0, time.UTC)
	w := s.Begin(Aircraft{Callsign: "AAL123", AircraftType: "A320", Rules: av.FlightRulesIFR,
		Flight: av.FlightTypeArrival, Departure: "KBOS", Arrival: "KJFK", Time: start})

	n := &nav.Nav{}
	n.FlightState.Altitude = 11000
	w.Start(n)

	w.SetOrigin("1F", true)
	w.Command("AssignHeading", math.MagneticHeading(270), av.TurnLeft, nav.NewTime(start), time.Duration(0))
	w.ClearOrigin()
	w.Lookup("waypoint", "ROBUC", nav.LookupResult[math.Point2LL]{Value: math.Point2LL{-73.5, 40.5}, OK: true})

	dir10, gust := 270, 25
	metar := &wx.METAR{WindDir: &dir10, WindSpeed: 15, WindGust: &gust}
	wxs := wx.MakeSample([2]float32{0.01, -0.02}, 7, -2, 1012)
	for i := range 3 {
		if i == 2 {
			metar = &wx.METAR{WindSpeed: 3} // variable
		}
		n.FlightState.Altitude -= 100
		w.Update(nav.UpdateRecord{
			Time: nav.NewTime(start.Add(time.Duration(i+1) * time.Second)), Weather: wxs, METAR: metar,
			Rules: av.FlightRulesIFR, State: n.FlightState,
			Result: nav.UpdateResult{PassedWaypoint: &av.Waypoint{Fix: "ROBUC"}},
		})
	}
	w.SetOrigin("4N", false)
	w.Command("DescendViaSTAR", (*float32)(nil), nav.NewTime(start.Add(3*time.Second)), time.Duration(0))
	if err := w.End("landed", start.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if base := filepath.Base(w.Path()); !strings.HasPrefix(base, "N90_20") || !strings.HasSuffix(base, "_AAL123"+HumanExt) {
		t.Errorf("a human-controlled trace was written to %s", w.Path())
	}

	tr, err := Load(w.Path())
	if err != nil {
		t.Fatal(err)
	}
	h := tr.Header
	if h.Callsign != "AAL123" || h.Rules != "IFR" || h.Flight != "arrival" || h.Facility != "N90" ||
		h.Revision != "abc123" || h.Departure != "KBOS" {
		t.Errorf("header: %+v", h)
	}
	var back nav.Nav
	if err := json.Unmarshal(h.Nav, &back); err != nil || back.FlightState.Altitude != 11000 {
		t.Errorf("header nav: %v, altitude %v", err, back.FlightState.Altitude)
	}

	var kinds []string
	for _, r := range tr.Records {
		switch {
		case r.Command != nil:
			kinds = append(kinds, "cmd "+r.Command.Name)
		case r.Lookup != nil:
			kinds = append(kinds, "lookup "+r.Lookup.Kind+" "+r.Lookup.Key)
		case r.Tick != nil:
			kinds = append(kinds, "tick")
		case r.End != nil:
			kinds = append(kinds, "end "+r.End.Reason)
		}
	}
	want := "cmd AssignHeading|lookup waypoint ROBUC|tick|tick|tick|cmd DescendViaSTAR|end landed"
	if got := strings.Join(kinds, "|"); got != want {
		t.Errorf("records: %s\nwant:    %s", got, want)
	}

	cmd := tr.Records[0].Command
	if cmd.Controller != "1F" || !cmd.Human || len(cmd.Args) != 4 || string(cmd.Args[0]) != "270" ||
		!cmd.Time.Equal(start) {
		t.Errorf("command: %+v", cmd)
	}
	if cmd := tr.Records[5].Command; cmd.Controller != "4N" || cmd.Human {
		t.Errorf("virtual controller's command: %+v", cmd)
	}

	ticks := []*Tick{tr.Records[2].Tick, tr.Records[3].Tick, tr.Records[4].Tick}
	if ticks[0].METAR == nil || ticks[1].METAR != nil || ticks[2].METAR == nil {
		t.Errorf("METAR recorded on ticks %v %v %v; want on the first and when it changes",
			ticks[0].METAR != nil, ticks[1].METAR != nil, ticks[2].METAR != nil)
	}
	if m := ticks[0].METAR; m.Direction == nil || *m.Direction != 270 || m.Speed != 15 || m.Gust == nil {
		t.Errorf("first METAR: %+v", m)
	}
	if ticks[2].METAR.Direction != nil {
		t.Errorf("variable wind recorded with a direction: %+v", ticks[2].METAR)
	}
	if ticks[0].Passed != "ROBUC" || ticks[0].Altitude != 10900 || ticks[0].Temperature != 7 ||
		ticks[0].Wind != wxs.WindVec() {
		t.Errorf("first tick: %+v", ticks[0])
	}
	if sample := wx.MakeSample(ticks[0].Wind, ticks[0].Temperature, ticks[0].Dewpoint, ticks[0].Pressure); sample != wxs {
		t.Errorf("weather doesn't round trip: %v, want %v", sample, wxs)
	}

	end := tr.End()
	if end == nil || len(end.Controllers) != 2 || end.Controllers[0].Name != "1F" || !end.Controllers[0].Human ||
		end.Controllers[0].Commands != 1 || end.Controllers[1].Human {
		t.Errorf("end: %+v", end)
	}
	if !tr.HumanControlled() {
		t.Error("not human controlled")
	}
}

// A Writer whose Nav never reported leaves no file; one still open when the
// session closes is ended by it; and a second trace for the same callsign in
// the same second gets its own file.
func TestSessionClose(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSession(dir, "PHL", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 6, 2, 30, 0, 0, time.UTC)
	ac := Aircraft{Callsign: "N123AB", Rules: av.FlightRulesVFR, Flight: av.FlightTypeDeparture, Time: now}

	silent := s.Begin(ac)
	flying := s.Begin(ac)
	flying.Start(&nav.Nav{})
	again := s.Begin(ac)
	again.Start(&nav.Nav{})
	if err := again.End("exited", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := silent.End("exited", now); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 || !strings.HasPrefix(names[0], "PHL_20") || !strings.Contains(names[0], "_N123AB") ||
		!strings.Contains(names[1], "_N123AB") || names[0] == names[1] {
		t.Errorf("files: %v, want two named by facility, time, and callsign", names)
	}

	tr, err := Load(flying.Path())
	if err != nil {
		t.Fatal(err)
	}
	if end := tr.End(); end == nil || end.Reason != "session ended" || !end.Time.Equal(now.Add(time.Minute)) {
		t.Errorf("end: %+v", end)
	}
	if tr.HumanControlled() {
		t.Error("human controlled with no commands")
	}
}

func TestSessionDirectories(t *testing.T) {
	start := time.Date(2026, 10, 6, 2, 30, 5, 0, time.UTC)
	if got, want := SessionDirName(start, "N90", "KJFK Depart 4L / Land 4L/R"), "2026-10-06T02-30-05Z_N90_KJFK-Depart-4L-Land-4L-R"; got != want {
		t.Errorf("SessionDirName = %q, want %q", got, want)
	}

	root := filepath.Join(t.TempDir(), "navtraces")
	if err := PruneSessions(root, 2); err != nil {
		t.Errorf("pruning a missing root: %v", err)
	}
	for _, d := range []string{"2026-10-06T02_A_x", "2026-10-07T02_B_x", "2026-10-05T02_C_x"} {
		if _, err := NewSession(filepath.Join(root, d), "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := PruneSessions(root, 2); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(root)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"2026-10-06T02_A_x", "2026-10-07T02_B_x"}; strings.Join(names, " ") != strings.Join(want, " ") {
		t.Errorf("kept %v, want %v", names, want)
	}
}

func TestSave(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSession(dir, "PHL", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 6, 2, 30, 0, 0, time.UTC)
	w := s.Begin(Aircraft{Callsign: "DAL1", Rules: av.FlightRulesIFR, Flight: av.FlightTypeArrival, Time: now})
	w.Start(&nav.Nav{})
	w.Command("ExpediteDescent")
	w.Update(nav.UpdateRecord{Time: nav.NewTime(now.Add(time.Second))})
	if err := w.End("landed", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	tr, err := Load(w.Path())
	if err != nil {
		t.Fatal(err)
	}
	tr.Records[1].Tick.Altitude = 1234
	path := filepath.Join(dir, "copy"+Ext)
	if err := tr.Save(path); err != nil {
		t.Fatal(err)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Header.Callsign != "DAL1" || len(again.Records) != 3 || again.Records[1].Tick.Altitude != 1234 ||
		again.End() == nil {
		t.Errorf("saved trace reads back as %+v", again)
	}
	if tmp, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(tmp) > 0 {
		t.Errorf("temporary files left: %v", tmp)
	}
}

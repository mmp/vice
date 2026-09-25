// simlog/simlog_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package simlog

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/wx"

	"github.com/vmihailenco/msgpack/v5"
)

var testStart = time.Date(2025, 7, 28, 12, 0, 0, 0, time.UTC)

func sample(callsign string, alt float32) Sample {
	return Sample{Callsign: callsign, Position: math.Point2LL{-75.2, 39.9}, Altitude: alt, IAS: 210, GS: 230, Heading: 270}
}

// writeTestLog writes a log with one record of each kind and returns its path.
func writeTestLog(t *testing.T, close bool) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.simlog")
	w, err := Create(path, Header{Facility: "PHL", Scenario: "test", SimStart: testStart}, []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	w.Weather(Weather{Time: testStart, Update: wx.AtmosUpdate{Atmos: &wx.AtmosByPointSOA{Lat: []float32{39.9}}, Time: testStart}})
	args, err := msgpack.Marshal(struct{ Commands string }{"C50"})
	if err != nil {
		t.Fatal(err)
	}
	input, err := msgpack.Marshal([]string{"METAR"})
	if err != nil {
		t.Fatal(err)
	}
	w.Request(Request{Time: testStart, TCW: "1N", Method: "Sim.RunAircraftCommands", Args: args,
		Inputs: []msgpack.RawMessage{input}, Error: "unable", Aircraft: "AAL1", Summary: "C50"})
	w.Spawn(Spawn{Time: testStart, Callsign: "AAL1", AircraftType: "A320"})
	for i := range 100 {
		w.Tick(Tick{Time: testStart.Add(time.Duration(i+1) * time.Second),
			Aircraft: []Sample{sample("AAL1", float32(3000+i))}})
	}
	w.Delete(Delete{Time: testStart.Add(100 * time.Second), Callsign: "AAL1", Reason: "landed"})

	if close {
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestLogRoundTrip(t *testing.T) {
	s, err := Load(writeTestLog(t, true))
	if err != nil {
		t.Fatal(err)
	}

	if s.Header.Facility != "PHL" || s.Header.Version != FormatVersion || !s.Header.SimStart.Equal(testStart) {
		t.Errorf("header %+v", s.Header)
	}
	if string(s.Snapshot) != "snapshot" {
		t.Errorf("snapshot %q", s.Snapshot)
	}

	kinds := make(map[Kind]int)
	for _, e := range s.Events {
		kinds[e.Kind]++
	}
	if kinds[KindWeather] != 1 || kinds[KindRequest] != 1 || kinds[KindSpawn] != 1 ||
		kinds[KindTick] != 100 || kinds[KindDelete] != 1 {
		t.Fatalf("read back records %v", kinds)
	}

	r := s.Events[1].Request
	var args struct{ Commands string }
	if err := msgpack.Unmarshal(r.Args, &args); err != nil || args.Commands != "C50" {
		t.Errorf("request arguments %+v, error %v", args, err)
	}
	var input []string
	if len(r.Inputs) != 1 || msgpack.Unmarshal(r.Inputs[0], &input) != nil || !slices.Equal(input, []string{"METAR"}) {
		t.Errorf("request inputs %v", r.Inputs)
	}
	if r.Error != "unable" || r.Summary != "C50" || r.TCW != "1N" || !r.Time.Equal(testStart) {
		t.Errorf("request %+v", r)
	}
	if lat := s.Events[0].Weather.Update.Atmos.Lat; !slices.Equal(lat, []float32{39.9}) {
		t.Errorf("weather %v", lat)
	}
	if tick := s.Events[len(s.Events)-2].Tick; tick.Aircraft[0] != sample("AAL1", 3099) {
		t.Errorf("last tick %+v", tick)
	}

	h, err := ReadHeader(writeTestLog(t, true))
	if err != nil || h.Scenario != "test" {
		t.Errorf("header %+v, error %v", h, err)
	}
}

// A log whose writer never closed it, as when the process running the
// session exits, reads back up to the last record it flushed.
func TestLogUnclosed(t *testing.T) {
	s, err := Load(writeTestLog(t, false))
	if err != nil {
		t.Fatal(err)
	}
	ticks := 0
	for _, e := range s.Events {
		if e.Kind == KindTick {
			ticks++
		}
	}
	if ticks != 90 {
		t.Errorf("read %d ticks from an unclosed log; want the 90 flushed", ticks)
	}
}

// So does one cut off partway through a record.
func TestLogTruncated(t *testing.T) {
	path := writeTestLog(t, true)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b[:len(b)*2/3], 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Events) == 0 || s.Events[0].Kind != KindWeather {
		t.Errorf("read %d events from a truncated log", len(s.Events))
	}
}

// session builds a log in memory from the given ticks, each a list of
// samples, and requests.
func session(ticks [][]Sample, requests ...Request) *Session {
	s := &Session{Header: Header{SimStart: testStart}}
	for _, r := range requests {
		s.Events = append(s.Events, Event{Kind: KindRequest, Request: &r})
	}
	for i, samples := range ticks {
		s.Events = append(s.Events, Event{Kind: KindTick,
			Tick: &Tick{Time: testStart.Add(time.Duration(i+1) * time.Second), Aircraft: samples}})
	}
	return s
}

func TestCompare(t *testing.T) {
	tol := Tolerances{Position: 0.05, Altitude: 25, Speed: 2, Heading: 2}
	golden := session([][]Sample{
		{sample("AAL1", 3000), sample("DAL2", 5000)},
		{sample("AAL1", 3050), sample("DAL2", 5000)},
		{sample("AAL1", 3100), sample("DAL2", 5000)},
	}, Request{Time: testStart, TCW: "1N", Aircraft: "AAL1", Summary: "C50"})

	if r := Compare(golden, golden, Tolerances{}); r.Diverged() {
		t.Errorf("a log differs from itself: %+v", r)
	}

	// Within tolerance at the second tick, beyond it at the third.
	replay := session([][]Sample{
		{sample("AAL1", 3000), sample("DAL2", 5000)},
		{sample("AAL1", 3060), sample("DAL2", 5000)},
		{sample("AAL1", 3200), sample("DAL2", 5000)},
	}, Request{Time: testStart, TCW: "1N", Aircraft: "AAL1", Summary: "C50", Error: "unable"})
	r := Compare(golden, replay, tol)
	if len(r.Aircraft) != 1 || r.Aircraft[0].Callsign != "AAL1" {
		t.Fatalf("reported %+v, want only AAL1", r.Aircraft)
	}
	ar := r.Aircraft[0]
	if ar.Divergence == nil || !ar.Divergence.Time.Equal(testStart.Add(3*time.Second)) {
		t.Errorf("divergence %+v, want at the third tick", ar.Divergence)
	}
	if len(ar.Requests) != 1 || !ar.Requests[0].Differs() {
		t.Errorf("requests %+v, want the one refused in the replay", ar.Requests)
	}

	// An aircraft that stops flying early in the replay diverges when it
	// does; one that only the replay has is reported as such. A request the
	// replay never got to counts against its aircraft.
	replay = session([][]Sample{
		{sample("AAL1", 3000), sample("DAL2", 5000), sample("UAL3", 7000)},
		{sample("AAL1", 3050), sample("UAL3", 7000)},
		{sample("AAL1", 3100), sample("UAL3", 7000)},
	})
	r = Compare(golden, replay, tol)
	var callsigns []string
	for _, ar := range r.Aircraft {
		callsigns = append(callsigns, ar.Callsign)
	}
	if !slices.Equal(callsigns, []string{"AAL1", "UAL3", "DAL2"}) {
		t.Fatalf("reported %v, want AAL1, UAL3, then DAL2", callsigns)
	}
	if !r.Aircraft[0].Requests[0].ReplayMissing {
		t.Errorf("AAL1's request %+v not reported missing from the replay", r.Aircraft[0].Requests[0])
	}
	if ar := r.Aircraft[1]; !ar.ReplayOnly || ar.Divergence == nil || ar.Divergence.Session != nil {
		t.Errorf("UAL3 %+v not reported as only in the replay", ar)
	}
	if d := r.Aircraft[2].Divergence; d == nil || d.Replay != nil || !d.Time.Equal(testStart.Add(2*time.Second)) {
		t.Errorf("DAL2 divergence %+v", d)
	}

	if r := Compare(golden, session(nil), tol); !r.Diverged() || r.ReplayTicks != 0 {
		t.Errorf("an empty replay doesn't diverge: %+v", r)
	}
}

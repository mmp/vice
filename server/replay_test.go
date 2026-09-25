// server/replay_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/scenario"
	"github.com/mmp/vice/simlog"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/vmihailenco/msgpack/v5"
)

// A session run the way a server runs one, with its update loop ticking in
// the background while a controller's requests arrive between ticks, has to
// replay exactly: the same aircraft flying the same paths, and the sim
// answering each request the same way.
func TestSessionReplay(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a sim for several minutes of sim time")
	}

	lg, sm := makeReplayTestSimManager(t)
	sd := &dispatcher{sm: sm}

	req := makeTestSimRequest(t, sm, "PHL", "KPHL Depart 27L Land 27R/35")
	var result NewSimResult
	if err := sm.NewSim(&req, &result); err != nil {
		t.Fatal(err)
	}
	token := result.ControllerToken
	if err := sd.SetSimRate(&SetSimRateArgs{ControllerToken: token, Rate: 50}, nil); err != nil {
		t.Fatal(err)
	}

	// Work the traffic for a few seconds of wall clock time, which is
	// several minutes of the sim's at that rate.
	commands := []string{"C40", "H270", "S210", "E27R", "D100 S250", "C27R", "L360", "R090", "SMIN", "H",
		"C60 TS180", "ID", "E35 C35"}
	const iterations = 60
	for i := range iterations {
		var update SimStateUpdate
		if err := sd.GetStateUpdate(token, &update); err != nil {
			t.Fatal(err)
		}
		callsigns := util.SortedMapKeys(update.Tracks)
		callsigns = slices.DeleteFunc(callsigns, func(cs av.ADSBCallsign) bool {
			return strings.HasPrefix(string(cs), "__") // unsupported datablocks
		})
		if len(callsigns) > 0 {
			var cr AircraftCommandsResult
			if err := sd.RunAircraftCommands(&AircraftCommandsArgs{
				ControllerToken: token,
				Callsign:        callsigns[i%len(callsigns)],
				Commands:        commands[i%len(commands)],
				ClickedTrack:    true,
			}, &cr); err != nil {
				t.Fatal(err)
			}
		}
		var contact RequestContactResult
		if err := sd.RequestContactTransmission(&RequestContactArgs{ControllerToken: token}, &contact); err != nil {
			t.Fatal(err)
		}
		if i == iterations/2 {
			if err := sd.FastForward(token, &update); err != nil {
				t.Fatal(err)
			}
		}
		if i == 10 {
			// Adding an airport's METAR reads the resources, which the
			// replay mustn't do.
			args := &AddMETARAirportArgs{ControllerToken: token, Airport: "KJFK"}
			if err := sd.AddMETARAirport(args, nil); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Signing off pauses the sim, so the log ends there.
	session := sm.sessionsByName[req.NewSimName]
	if err := sd.SignOff(token, nil); err != nil {
		t.Fatal(err)
	}
	session.closeLog()

	logs, err := filepath.Glob(filepath.Join(lg.LogDir, "simlogs", "*.simlog"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("found session logs %v (error %v), want one", logs, err)
	}
	sess, err := simlog.Load(logs[0])
	if err != nil {
		t.Fatal(err)
	}

	counts := make(map[simlog.Kind]int)
	for _, e := range sess.Events {
		counts[e.Kind]++
	}
	if counts[simlog.KindTick] < 100 || counts[simlog.KindRequest] < 30 || counts[simlog.KindSpawn] == 0 {
		t.Fatalf("session log has %d ticks, %d requests, and %d spawns; the session didn't run as expected",
			counts[simlog.KindTick], counts[simlog.KindRequest], counts[simlog.KindSpawn])
	}
	if sess.Header.Scenario != req.ScenarioName || sess.Header.Facility != req.Facility {
		t.Errorf("header names %s/%s, want %s/%s", sess.Header.Facility, sess.Header.Scenario,
			req.Facility, req.ScenarioName)
	}
	metar := requestInputs(sess, AddMETARAirportRPC)
	var reports []wx.METAR
	if len(metar) != 1 || msgpack.Unmarshal(metar[0], &reports) != nil || len(reports) == 0 {
		t.Fatalf("session log records METAR inputs %v, want one with the airport's reports", metar)
	}

	// The replay runs on the database the log names, as read back from its
	// copy beside the log.
	d, err := simlog.LoadDatabase(filepath.Dir(logs[0]), sess.Header.Database)
	if err != nil {
		t.Fatal(err)
	}
	orig := db.DB
	db.DB = d
	defer func() { db.DB = orig }()

	replay := func() *simlog.Session {
		path := filepath.Join(t.TempDir(), "replay.simlog")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		w, err := simlog.NewWriter(f, sess.Header, sess.Snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if err := Replay(sess, w, lg); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		r, err := simlog.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	first := replay()
	checkSameFlights(t, "session and replay", sess, first)
	if got := requestInputs(first, AddMETARAirportRPC); len(got) != 1 || !bytes.Equal(got[0], metar[0]) {
		t.Errorf("replay recorded METAR inputs %v, want the session's", got)
	}
	second := replay()
	checkSameFlights(t, "two replays", first, second)
}

// requestInputs returns what the first request s records for method read.
func requestInputs(s *simlog.Session, method string) []msgpack.RawMessage {
	for _, e := range s.Events {
		if e.Kind == simlog.KindRequest && e.Request.Method == method {
			return e.Request.Inputs
		}
	}
	return nil
}

// A replayed request gets what the session's request read from the session
// log rather than reading it again, and the replay's own log records it.
func TestRecordInputReplays(t *testing.T) {
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	w, err := simlog.Create(filepath.Join(t.TempDir(), "replay.simlog"), simlog.Header{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	recorded, err := msgpack.Marshal([]string{"recorded"})
	if err != nil {
		t.Fatal(err)
	}
	ss := &simSession{lg: lg, log: w, replaying: true, replayInputs: []msgpack.RawMessage{recorded}}
	got := recordInput(ss, func() []string {
		t.Error("the replay read the input again")
		return nil
	})
	if !slices.Equal(got, []string{"recorded"}) {
		t.Errorf("replayed input %v, want the recorded one", got)
	}
	if len(ss.request.Inputs) != 1 || !bytes.Equal(ss.request.Inputs[0], recorded) {
		t.Errorf("replay's log records inputs %v, want the recorded one", ss.request.Inputs)
	}
}

func checkSameFlights(t *testing.T, what string, a, b *simlog.Session) {
	t.Helper()
	if report := simlog.Compare(a, b, simlog.Tolerances{}); report.Diverged() {
		var s strings.Builder
		report.Write(&s)
		t.Errorf("%s differ:\n%s", what, s.String())
	}
}

// makeReplayTestSimManager returns a SimManager that runs the built-in
// scenarios and writes session logs to a temporary directory.
func makeReplayTestSimManager(t *testing.T) (*log.Logger, *SimManager) {
	t.Helper()

	db.InitDB()
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), LogDir: t.TempDir()}

	var e util.ErrorLogger
	tables, _ := scenario.Load(scenario.OverrideFiles{}, &e, lg)
	if e.HaveErrors() {
		t.Fatalf("loading scenarios: %s", e.String())
	}

	sm := &SimManager{
		sessionsByName:  make(map[string]*simSession),
		sessionsByToken: make(map[string]*simSession),
		providersReady:  make(chan struct{}),
		lg:              lg,
	}
	sm.scenarios.Store(tables)
	// With no server to ask, weather comes from the bundled resources.
	sm.wxProvider = wx.MakeProvider("", lg)
	close(sm.providersReady)

	return lg, sm
}

// makeTestSimRequest returns a request for a new sim running the given
// scenario with its default traffic, starting when the resources have
// weather for the facility.
func makeTestSimRequest(t *testing.T, sm *SimManager, facility, scenarioName string) NewSimRequest {
	t.Helper()

	tables := sm.scenarios.Load()
	for groupName, catalog := range util.SortedMap(tables.Catalogs[facility]) {
		spec, ok := catalog.Scenarios[scenarioName]
		if !ok {
			continue
		}

		req := MakeNewSimRequest()
		req.Facility, req.GroupName, req.ScenarioName = facility, groupName, scenarioName
		req.ScenarioSpec = spec
		req.Initials = "XX"
		req.Privileged = true // so every aircraft takes commands
		intervals := wx.GetTRACONTimeIntervals()[facility]
		if len(intervals) == 0 {
			intervals = wx.GetARTCCTimeIntervals()[facility]
		}
		if len(intervals) == 0 {
			t.Fatalf("%s: no weather in the resources", facility)
		}
		req.StartTime = intervals[0].Start().Add(2 * time.Hour)
		if src := &req.ScenarioSpec.LaunchConfig.TrafficSource; !slices.Contains(spec.TrafficSources, *src) {
			*src = spec.TrafficSources[0]
		}
		return req
	}
	t.Fatalf("%s/%s: no such scenario", facility, scenarioName)
	return NewSimRequest{}
}

// Every Sim RPC is either recorded in session logs or deliberately left out
// of them, and what is recorded leaves out the controller's token and how the
// speech recognizer heard them. Each RPC is called with empty arguments; the
// sim refuses most of them, and a refused request is recorded too.
func TestDispatcherRequestsRecorded(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a sim")
	}

	unrecorded := map[string]string{
		GetStateUpdateRPC:             "a query",
		GetAircraftDisplayStateRPC:    "a query",
		GetMapLibraryRPC:              "a query",
		SignOffRPC:                    "recorded as " + signOffMethod + " when the last controller at a TCW leaves",
		SetSimRateRPC:                 "the replay steps the sim tick by tick",
		TogglePauseRPC:                "the replay steps the sim tick by tick",
		FastForwardRPC:                "its ticks are recorded",
		RecordFlightsRPC:              "its ticks are recorded",
		GlobalMessageRPC:              "chat between controllers",
		UpdateATISGITextRPC:           "free text that nothing flies by",
		AnnotateFlightStripRPC:        "free text that nothing flies by",
		RequestContactTransmissionRPC: "recorded only when a contact is waiting, as TestSessionReplay checks",
	}

	lg, sm := makeReplayTestSimManager(t)
	req := makeTestSimRequest(t, sm, "PHL", "KPHL Depart 27L Land 27R/35")
	var result NewSimResult
	if err := sm.NewSim(&req, &result); err != nil {
		t.Fatal(err)
	}
	token := result.ControllerToken
	const transcript = "a voice transcript"

	sd := &dispatcher{sm: sm}
	var called []string
	for m := range reflect.TypeFor[*dispatcher]().Methods() {
		method := "Sim." + m.Name
		if _, ok := unrecorded[method]; ok {
			continue
		}
		arg := reflect.ValueOf(token)
		if argType := m.Type.In(1); argType.Kind() != reflect.String {
			arg = reflect.New(argType.Elem())
			setField(arg.Elem(), "ControllerToken", token)
			setField(arg.Elem(), "WhisperTranscript", transcript)
		}
		m.Func.Call([]reflect.Value{reflect.ValueOf(sd), arg, reflect.New(m.Type.In(2).Elem())})
		called = append(called, method)
	}

	sm.sessionsByName[req.NewSimName].closeLog()
	logs, err := filepath.Glob(filepath.Join(lg.LogDir, "simlogs", "*.simlog"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("found session logs %v (error %v), want one", logs, err)
	}
	sess, err := simlog.Load(logs[0])
	if err != nil {
		t.Fatal(err)
	}

	recorded := make(map[string]bool)
	for _, e := range sess.Events {
		if e.Kind != simlog.KindRequest {
			continue
		}
		recorded[e.Request.Method] = true
		if args := string(e.Request.Args); strings.Contains(args, token) || strings.Contains(args, transcript) {
			t.Errorf("%s: recorded arguments include the controller's token or a voice transcript", e.Request.Method)
		}
	}
	for _, method := range called {
		if !recorded[method] {
			t.Errorf("%s: not recorded in the session log; record it or list it as deliberately not", method)
		}
	}
}

// setField sets the named string field of v, found in it or the structs it
// embeds, if there is one.
func setField(v reflect.Value, name, value string) {
	if f := v.FieldByName(name); f.IsValid() && f.Kind() == reflect.String {
		f.SetString(value)
	}
}

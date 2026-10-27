// server/manager_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"errors"
	"io"
	"path/filepath"
	"slices"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/navtrace"
	"github.com/mmp/vice/scenario"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"log/slog"
)

// A request names its scenario, and nothing has vetted the name by the time it
// arrives. A group is catalogued only if it has a scenario that can be flown,
// so neither the catalog nor the scenario in it can be taken on faith.
func TestMakeSimConfigurationRejectsUnknownScenario(t *testing.T) {
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	var sm SimManager
	sm.scenarios.Store(&scenario.Tables{
		Groups: map[string]map[string]*scenario.Group{
			"ABC": {"Group": {}},
		},
		Catalogs: map[string]map[string]*scenario.Catalog{
			"ABC": {"Group": {Scenarios: map[string]*scenario.Spec{"Known": {}}}},
		},
	})

	for _, tc := range []struct{ facility, group, scenario string }{
		{"ABC", "Group", "Unknown"}, // catalogued group, scenario that isn't in it
		{"ABC", "Uncatalogued", "Known"},
		{"XYZ", "Group", "Known"},
	} {
		req := &NewSimRequest{Facility: tc.facility, GroupName: tc.group, ScenarioName: tc.scenario,
			ScenarioSpec: &scenario.Spec{}}
		_, err := sm.makeSimConfiguration(req, lg)
		if !errors.Is(err, ErrInvalidSimConfiguration) {
			t.Errorf("%s/%s/%s: error is %v, want %v", tc.facility, tc.group, tc.scenario,
				err, ErrInvalidSimConfiguration)
		}
	}
}

// A scenario the catalog has can still be asked for with traffic it doesn't
// offer, which is the server's call to make and not the client's.
func TestMakeSimConfigurationRejectsUnofferedTraffic(t *testing.T) {
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	var sm SimManager
	sm.scenarios.Store(&scenario.Tables{
		Groups: map[string]map[string]*scenario.Group{
			"ABC": {"Group": {}},
		},
		Catalogs: map[string]map[string]*scenario.Catalog{
			"ABC": {"Group": {Scenarios: map[string]*scenario.Spec{
				"Known": {TrafficSources: []sim.TrafficSource{sim.TrafficSourceScenario}},
			}}},
		},
	})

	req := &NewSimRequest{Facility: "ABC", GroupName: "Group", ScenarioName: "Known",
		ScenarioSpec: &scenario.Spec{}}
	req.ScenarioSpec.LaunchConfig.TrafficSource = sim.TrafficSourceHistorical
	if _, err := sm.makeSimConfiguration(req, lg); !errors.Is(err, ErrInvalidTrafficSource) {
		t.Errorf("error is %v, want %v", err, ErrInvalidTrafficSource)
	}
}

// Every client that connects lists the running sims, and a session stays
// locked for as long as its sim takes over a request; one that never finished
// once wedged every connection to the server. The listing has to answer while
// a session is locked, with the consolidation its requests left.
func TestGetRunningSimsDoesNotWaitOnSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a sim")
	}

	_, sm := makeTestSimManager(t)
	req := makeTestSimRequest(t, sm, "PHL", "KPHL Depart 27L Land 27R/35")
	var result NewSimResult
	if err := sm.NewSim(&req, &result); err != nil {
		t.Fatal(err)
	}
	sd := &dispatcher{sm: sm}
	var update SimStateUpdate
	args := &DeconsolidateTCPArgs{ControllerToken: result.ControllerToken, TCP: "1S"}
	if err := sd.DeconsolidateTCP(args, &update); err != nil || update.SimErrorMessage != "" {
		t.Fatalf("deconsolidating 1S: %v %s", err, update.SimErrorMessage)
	}

	ss := sm.sessionsByName[req.NewSimName]
	ss.mu.Lock(ss.lg)
	defer ss.mu.Unlock(ss.lg)

	listed := make(chan map[string]*RunningSim, 1)
	go func() {
		var running map[string]*RunningSim
		if err := sm.GetRunningSims(0, &running); err != nil {
			t.Error(err)
		}
		listed <- running
	}()

	var running map[string]*RunningSim
	select {
	case running = <-listed:
	case <-time.After(5 * time.Second):
		t.Fatal("GetRunningSims waited on a locked session")
	}

	rs, ok := running[req.NewSimName]
	if !ok {
		t.Fatalf("%s not listed in %v", req.NewSimName, running)
	}
	if len(rs.ScenarioDefaultConsolidation) == 0 {
		t.Errorf("no default consolidation listed")
	}
	cons := rs.CurrentConsolidation["1N"]
	if !slices.Equal(cons.Initials, []string{"XX"}) ||
		slices.ContainsFunc(cons.SecondaryTCPs, func(s sim.SecondaryTCP) bool { return s.TCP == "1S" }) {
		t.Errorf("1N consolidation %+v, want XX signed in and 1S deconsolidated", cons)
	}
	if p := rs.CurrentConsolidation["1S"].PrimaryTCP; p != "1S" {
		t.Errorf("1S primary TCP %q, want 1S", p)
	}
}

// A sim that never finishes a request can hold up only what is waiting on it.
// Signing off from it, joining it, and reading the status page all wait on its
// lock, and none of them may hold the manager's while they do, lest everyone
// else wait too.
func TestStuckSessionDoesNotWedgeServer(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a sim")
	}

	_, sm := makeTestSimManager(t)
	req := makeTestSimRequest(t, sm, "PHL", "KPHL Depart 27L Land 27R/35")
	var result NewSimResult
	if err := sm.NewSim(&req, &result); err != nil {
		t.Fatal(err)
	}

	ss := sm.sessionsByName[req.NewSimName]
	ss.mu.Lock(ss.lg)
	defer ss.mu.Unlock(ss.lg)

	go sm.SignOff(result.ControllerToken)
	go sm.ConnectToSim(&JoinSimRequest{SimName: req.NewSimName, TCW: "1S", Initials: "YY"}, &NewSimResult{})
	go sm.GetSimStatus()
	// Give them time to reach the session's lock. Were one to hold the
	// manager's while it waited there, the calls below would never return.
	time.Sleep(250 * time.Millisecond)

	answered := make(chan struct{})
	go func() {
		var running map[string]*RunningSim
		if err := sm.GetRunningSims(0, &running); err != nil {
			t.Error(err)
		}
		sm.LookupController(result.ControllerToken)
		close(answered)
	}()
	select {
	case <-answered:
	case <-time.After(5 * time.Second):
		t.Fatal("the server waited on a stuck session")
	}
}

// Two controllers asking for the same TCW at once can't both get it: the TCW
// found free has to stay free until the controller who found it signs on.
func TestConcurrentSignOnsAtOneTCW(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a sim")
	}

	_, sm := makeTestSimManager(t)
	req := makeTestSimRequest(t, sm, "PHL", "KPHL Depart 27L Land 27R/35")
	var result NewSimResult
	if err := sm.NewSim(&req, &result); err != nil {
		t.Fatal(err)
	}

	errs := make(chan error)
	for _, initials := range []string{"YY", "ZZ"} {
		go func() {
			join := &JoinSimRequest{SimName: req.NewSimName, TCW: "1S", Initials: initials}
			errs <- sm.ConnectToSim(join, &NewSimResult{})
		}()
	}
	var signedOn, occupied int
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			signedOn++
		case errors.Is(err, ErrTCWAlreadyOccupied):
			occupied++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if signedOn != 1 || occupied != 1 {
		t.Errorf("%d signed on and %d found 1S occupied, want one of each", signedOn, occupied)
	}
}

// makeTestSimManager returns a SimManager over the scenarios in the
// resources, with no server to talk to.
func makeTestSimManager(t *testing.T) (*log.Logger, *SimManager) {
	t.Helper()

	db.InitDB()
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), LogDir: t.TempDir()}

	var e util.ErrorLogger
	tables := scenario.Load(&e, lg)
	if e.HaveErrors() {
		t.Fatalf("loading scenarios: %s", e.String())
	}

	sm := &SimManager{
		sessionsByName:  make(map[string]*simSession),
		sessionsByToken: make(map[string]*simSession),
		// With no server to ask, weather comes from the bundled resources.
		wxProvider: wx.MakeProvider("", lg),
		lg:         lg,
	}
	sm.scenarios.Store(tables)

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
		intervals := wx.FacilityTimeIntervals(facility)
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

// A session records each aircraft's flight to a trace under the log
// directory: the ticks it flies, the commands it is given with who gave
// them, and how it ended.
func TestSessionRecordsTraces(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a sim")
	}

	lg, sm := makeTestSimManager(t)
	req := makeTestSimRequest(t, sm, "PHL", "KPHL Depart 27L Land 27R/35")
	var result NewSimResult
	if err := sm.NewSim(&req, &result); err != nil {
		t.Fatal(err)
	}
	ss := sm.sessionsByName[req.NewSimName]

	// Let the aircraft fly a few ticks, then give one a heading.
	time.Sleep(2500 * time.Millisecond)
	var callsign av.ADSBCallsign
	ss.withSim(func() {
		for cs, ac := range util.SortedMap(ss.sim.Aircraft) {
			if ac.IsAirborne() {
				callsign = cs
				break
			}
		}
		if callsign == "" {
			t.Fatal("no airborne aircraft")
		}
		if res := ss.sim.RunScriptedControlCommands("1N", callsign, "H270"); res.Error != nil {
			t.Fatalf("%s: %v", callsign, res.Error)
		}
	})
	time.Sleep(1500 * time.Millisecond)
	ss.withSim(ss.sim.Destroy)

	paths, err := filepath.Glob(filepath.Join(lg.LogDir, "navtraces", "*", string(callsign)+navtrace.HumanExt))
	if err != nil || len(paths) != 1 {
		all, _ := filepath.Glob(filepath.Join(lg.LogDir, "navtraces", "*", "*"))
		t.Fatalf("trace of %s: %v %v; traces written: %v", callsign, paths, err, all)
	}
	tr, err := navtrace.Load(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if tr.Header.Facility != "PHL" || tr.Header.Callsign != string(callsign) {
		t.Errorf("header: %+v", tr.Header)
	}
	ticks, commands := 0, 0
	for _, r := range tr.Records {
		if r.Tick != nil {
			ticks++
		}
		if c := r.Command; c != nil && c.Name == "AssignHeading" {
			commands++
			if c.Controller != "1N" || !c.Human {
				t.Errorf("heading recorded from %q human %v", c.Controller, c.Human)
			}
		}
	}
	if ticks == 0 || commands != 1 {
		t.Errorf("%d ticks and %d heading commands recorded", ticks, commands)
	}
	if end := tr.End(); end == nil || end.Reason != "session ended" {
		t.Errorf("end: %+v", end)
	}
}

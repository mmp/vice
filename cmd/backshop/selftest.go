// cmd/backshop/selftest.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"slices"
	"time"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// runSelfTest does everything backshop does at startup apart from opening a
// window: it loads the scenarios, creates a sim, builds the maps, and
// reports what it found. It exercises the parts that can break from a
// scenario edit, so it is what to run in CI and on a machine with no
// display.
func runSelfTest(config *Config, lg *log.Logger) error {
	if err := initResources(&util.TextSyncUI{}); err != nil {
		return err
	}

	a, err := newApp(config, nil, nil, lg)
	if err != nil {
		return err
	}
	defer a.shutdown()

	if a.status != "" {
		return fmt.Errorf("%s", a.status)
	}

	catalogs := a.catalogs()
	scenarios := 0
	for _, groups := range catalogs {
		for _, catalog := range groups {
			scenarios += len(catalog.Scenarios)
		}
	}
	fmt.Printf("%d facilities, %d scenarios\n", len(catalogs), scenarios)
	if scenarios == 0 {
		return fmt.Errorf("no scenarios loaded")
	}

	// Creating the sim is asynchronous; pump the connection manager until
	// the client shows up.
	deadline := time.Now().Add(30 * time.Second)
	for a.cc == nil {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out starting %s / %s", a.sel.facility, a.sel.scenario)
		}
		a.mgr.Update(nil, lg)
		time.Sleep(10 * time.Millisecond)
	}

	ss := &a.cc.State
	fmt.Printf("%s / %s / %s: center %s, range %.0f nm\n", a.sel.facility, a.sel.group,
		a.sel.scenario, ss.Center.DMSString(), ss.Range)

	library, system, visible := 0, 0, 0
	for _, m := range a.scope.maps {
		if m.system {
			system++
		} else {
			library++
		}
		if m.visible {
			visible++
		}
	}
	fmt.Printf("%s: %d library maps, %d system maps, %d drawn by default\n",
		ss.ControllerVideoMapFile, library, system, visible)
	if defaults := len(ss.ControllerDefaultVideoMaps) + len(ss.ScenarioDefaultVideoMaps); visible == 0 && defaults > 0 {
		return fmt.Errorf("the scenario names default maps but none are drawn")
	}
	if library == 0 && ss.ControllerVideoMapFile != "" {
		return fmt.Errorf("%s: no maps loaded", ss.ControllerVideoMapFile)
	}

	fmt.Printf("%d airports, %d controllers, %d inbound flows, %d airspace positions\n",
		len(ss.Airports), len(ss.Controllers), len(ss.InboundFlows), len(ss.Airspace))
	fmt.Printf("departure airports: %v\n", util.SortedMapKeys(ss.DepartureAirports))
	fmt.Printf("arrival airports: %v\n", util.SortedMapKeys(ss.ArrivalAirports))

	if err := selfTestReports(a); err != nil {
		return err
	}
	if err := selfTestRecording(a); err != nil {
		return err
	}

	if a.reload = a.mgr.LocalServer.ReloadScenarios(a.reloadArgs()); a.reload != nil {
		start := time.Now()
		for !a.reload.Done() {
			time.Sleep(10 * time.Millisecond)
		}
		result, err := a.reload.Result()
		a.reload = nil
		if err != nil {
			return fmt.Errorf("reload: %w", err)
		}
		if len(result.Errors) > 0 {
			return fmt.Errorf("reload reported %d errors, first: %s", len(result.Errors), result.Errors[0])
		}
		fmt.Printf("reloaded scenarios in %s\n", time.Since(start).Round(time.Millisecond))
	}

	return nil
}

// selfTestReports exercises the Traffic and Routes tabs: it asks for the
// published traffic the scenario would fly and then for the routes of the
// first city pair it turns up, which is the whole of what those tabs do apart
// from drawing tables.
func selfTestReports(a *app) error {
	spec := a.selectedSpec()
	sources := publishedSources(spec)
	if len(sources) == 0 {
		fmt.Println("no published traffic for this scenario")
		return nil
	}

	t := &a.inspector.traffic
	t.query.Source = sources[0]
	t.defaultQuery(spec)
	t.fetch(a)
	if err := pumpUntil(a, func() bool { return t.run == nil }); err != nil {
		return fmt.Errorf("%s traffic: %w", t.query.Source, err)
	}
	if t.err != "" {
		return fmt.Errorf("%s traffic: %s", t.query.Source, t.err)
	}

	dropped, waiting := 0, 0
	for _, f := range t.report.Flights {
		switch f.Outcome {
		case sim.FlightDropped:
			dropped++
		case sim.FlightWaiting:
			waiting++
		}
	}
	fmt.Printf("%s traffic from %s: %d flights, %d with no route the scenario can fly, "+
		"%d waiting on a flow to be enabled\n", t.query.Source,
		t.query.Start.Format(startTimeFormat), len(t.report.Flights), dropped, waiting)
	if len(t.report.Flights) == 0 {
		return nil
	}

	r := &a.inspector.pairRoutes
	first := t.report.Flights[0]
	r.lookup(a, first.From, first.To)
	if r.err != "" {
		return fmt.Errorf("%s routes: %s", r.pair, r.err)
	}
	fmt.Printf("%s: %d routes\n", r.pair, len(r.routes.Routes))
	return nil
}

// pumpUntil runs the app's own update until outstanding work has come back:
// that is where RPC callbacks are invoked and where finished reports are taken
// in, so a test that waits any other way waits forever.
func pumpUntil(a *app, done func() bool) error {
	deadline := time.Now().Add(60 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out")
		}
		a.update()
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// selfTestRecording flies the first departure the scenario offers from end to
// end, which is what the Launch tab does and the longest-running thing the sim
// is asked for.
func selfTestRecording(a *app) error {
	slots := a.cc.State.DepartureLaunchSlots
	i := slices.IndexFunc(slots, func(s sim.DepartureLaunchSlot) bool { return s.Callsign != "" })
	if i == -1 {
		fmt.Println("no departure slot to launch")
		return nil
	}

	p := &a.inspector.playback
	flight := slots[i].LaunchFlight
	flight.ImmediateTakeoff = true
	start := time.Now()
	p.launch(a, flight)
	if err := pumpUntil(a, func() bool { return !p.recording }); err != nil {
		return fmt.Errorf("recording %s: %w", flight.Callsign, err)
	}
	if p.empty() {
		return fmt.Errorf("%s: nothing was recorded", flight.Callsign)
	}
	flown := time.Duration(0)
	for _, f := range p.flights {
		flown = max(flown, f.End().Sub(f.Start))
	}
	fmt.Printf("recorded %d flights, the longest %s, in %s\n", len(p.flights),
		clockText(flown), time.Since(start).Round(time.Millisecond))
	return nil
}

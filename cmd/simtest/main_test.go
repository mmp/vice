// cmd/simtest/main_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/mmp/vice/simlog"
)

func TestSummarize(t *testing.T) {
	start := time.Date(2026, 9, 27, 14, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return start.Add(time.Duration(s) * time.Second) }
	spawn := func(s int, callsign, rules, flight string) simlog.Event {
		return simlog.Event{Kind: simlog.KindSpawn,
			Spawn: &simlog.Spawn{Time: at(s), Callsign: callsign, Rules: rules, Flight: flight}}
	}
	remove := func(s int, callsign, reason string) simlog.Event {
		return simlog.Event{Kind: simlog.KindDelete, Delete: &simlog.Delete{Time: at(s), Callsign: callsign, Reason: reason}}
	}
	request := func(s int, tcw, method, callsign, err string) simlog.Event {
		return simlog.Event{Kind: simlog.KindRequest,
			Request: &simlog.Request{Time: at(s), TCW: tcw, Method: method, Aircraft: callsign, Error: err}}
	}
	tick := func(s int, callsigns ...string) simlog.Event {
		tk := &simlog.Tick{Time: at(s)}
		for _, cs := range callsigns {
			tk.Aircraft = append(tk.Aircraft, simlog.Sample{Callsign: cs})
		}
		return simlog.Event{Kind: simlog.KindTick, Tick: tk}
	}

	sess := &simlog.Session{
		Header: simlog.Header{Facility: "PHL", Scenario: "KPHL 27L", ScenarioGroup: "KPHL", Start: start, SimStart: start,
			Build: simlog.Build{Revision: "a5dfc5a34"}},
		Events: []simlog.Event{
			request(0, "1N", "SignOn", "", ""),
			spawn(0, "AAL1", "IFR", "arrival"),
			spawn(0, "DAL2", "IFR", "departure"),
			spawn(0, "N123", "VFR", "departure"),
			tick(1, "AAL1", "N123"),
			request(1, "1N", "Sim.RunAircraftCommands", "AAL1", ""),
			// A refused request doesn't count as working the aircraft.
			request(1, "1N", "Sim.RunAircraftCommands", "N123", "unable"),
			// Nor does launching it.
			spawn(2, "UAL3", "IFR", "departure"),
			request(2, "1N", "Sim.LaunchAircraft", "UAL3", ""),
			tick(2, "AAL1", "N123", "UAL3"),
			remove(2, "AAL1", "landed"),
			// A callsign may come back after the aircraft that had it left.
			spawn(3, "AAL1", "IFR", "overflight"),
			request(3, "2K", "Sim.AcceptHandoff", "AAL1", ""),
			tick(3, "N123", "UAL3", "AAL1"),
			remove(3, "N123", "culled"),
		},
	}

	var b strings.Builder
	summarize(&b, sess)
	want := `PHL KPHL 27L (KPHL), recorded 2026-09-27 14:00Z by a5dfc5a34
Ran 3s of sim time, 14:00:00 to 14:00:03.
                     IFR         IFR         VFR          IFR
                arrivals  departures  departures  overflights  total
  aircraft             1           2           1            1      5
  worked               1           0           0            1      2
  culled               0           0           1            0      1
  landed               1           0           0            0      1
  still flying         0           1           0            1      2
  never flew           0           1           0            0      1
Controllers at 1N, 2K made 5 requests (1 refused):
  2 RunAircraftCommands (1 refused)
  1 AcceptHandoff
  1 LaunchAircraft
  1 SignOn
`
	if b.String() != want {
		t.Errorf("summary:\n%s\nwant:\n%s", b.String(), want)
	}
}

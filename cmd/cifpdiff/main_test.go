// cmd/cifpdiff/main_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
)

func TestDiffProcedures(t *testing.T) {
	route := func(fixes ...string) av.WaypointArray {
		var wps av.WaypointArray
		for _, f := range fixes {
			wps = append(wps, av.Waypoint{Fix: f})
		}
		return wps
	}
	sid := func(fixes ...string) av.SID { return av.SID{Common: route(fixes...)} }
	cifp := func(sids map[string]av.SID, approaches map[string]av.Approach) db.ARINC424Result {
		return db.ARINC424Result{Airports: map[av.ICAOAirportCode]db.Airport{
			"KXYZ": {SIDs: sids, Approaches: approaches},
		}}
	}

	old := cifp(map[string]av.SID{
		"SAME1":  sid("AAAAA"),
		"RENUM4": sid("BBBBB", "CCCCC"),
		"MOVED2": sid("DDDDD"),
		"GONE3":  sid("EEEEE"),
	}, map[string]av.Approach{
		"I27": {Waypoints: []av.WaypointArray{route("FFFFF", "GGGGG")}},
		"S9":  {Waypoints: []av.WaypointArray{route("HHHHH")}},
	})
	cur := cifp(map[string]av.SID{
		"SAME1":  sid("AAAAA"),
		"RENUM5": sid("BBBBB", "CCCCC"),
		"MOVED3": sid("DDDDD", "JJJJJ"),
		"NEW1":   sid("KKKKK"),
	}, map[string]av.Approach{
		"I27": {Waypoints: []av.WaypointArray{route("FFFFF", "LLLLL")}},
		"S27": {Waypoints: []av.WaypointArray{route("HHHHH")}},
	})

	type summary struct {
		kind, old, new string
		same           bool
	}
	var got []summary
	for _, c := range diffProcedures(old, cur) {
		got = append(got, summary{c.Kind, c.Old, c.New, sameRoutes(c.Old, c.OldRoutes, c.New, c.NewRoutes)})
	}
	want := []summary{
		{"SID", "GONE3", "", false},
		{"SID", "MOVED2", "MOVED3", false},
		{"SID", "RENUM4", "RENUM5", true},
		{"SID", "", "NEW1", false},
		{"approach", "I27", "I27", false},
		{"approach", "S9", "", false},
		{"approach", "", "S27", false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("change %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// scenario/scenario_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scenario

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/enroute"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// TestValidateCoordinationFixes verifies that a coordination fix the consuming
// facility can't match is reported at load rather than deriving an exit fix
// that nothing downstream matches.
func TestValidateCoordinationFixes(t *testing.T) {
	fa := &sim.FacilityAdaptation{
		SignificantPoints: map[string]sim.SignificantPoint{
			"BOSOX":  {ShortName: "BOX"},
			"ROBUCC": {},
			"PVD":    {},
		},
	}

	coordination := func(fixes ...string) *enroute.Coordination {
		var coordFixes []enroute.CoordFix
		for _, f := range fixes {
			coordFixes = append(coordFixes, enroute.CoordFix{Fix: f})
		}
		return &enroute.Coordination{
			ComputerID: "BOA",
			Coord: &enroute.ArtsCoordEntry{
				RouteBased: []enroute.RouteRule{{Type: "string", ID: "X", Fixes: coordFixes}},
				ZoneBased: []enroute.ZoneArea{{AreaID: "Z1",
					Departure: []enroute.ZoneEntry{{DefaultFix: fixes[0]}}}},
			},
		}
	}

	for _, tc := range []struct {
		name    string
		ec      *enroute.Coordination
		wantErr string
	}{
		{"adapted fixes are accepted", coordination("BOSOX", "ROBUCC", "PVD"), ""},
		{"an unadapted fix is reported", coordination("NTELL"), "NTELL"},
		{"no coordination adapted", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var e util.ErrorLogger
			validateCoordinationFixes(tc.ec, fa, "A90", &e)

			errs := slices.Collect(e.Errors())
			if tc.wantErr == "" {
				if len(errs) > 0 {
					t.Errorf("unexpected errors: %v", errs)
				}
				return
			}
			if len(errs) == 0 {
				t.Fatalf("no error reported for %s", tc.wantErr)
			}
			// The message must name the offending fix and locate it, so a
			// facility engineer can find it.
			for _, want := range []string{tc.wantErr, "A90", "arts_coordination[BOA]"} {
				if !strings.Contains(errs[0], want) {
					t.Errorf("error %q does not mention %q", errs[0], want)
				}
			}
		})
	}
}

// TestAirportFiltersCoverTheField verifies that the default airport filter
// regions include an aircraft on the ground at the airport, including at
// fields at or below sea level.
func TestAirportFiltersCoverTheField(t *testing.T) {
	oldDB := db.DB
	db.DB = &db.StaticDatabase{
		Airports: map[av.ICAOAirportCode]db.Airport{
			"KTRM": {Id: "KTRM", Elevation: -114, Location: math.Point2LL{-116.16, 33.63}},
			"KMSY": {Id: "KMSY", Elevation: 0, Location: math.Point2LL{-90.26, 29.99}},
			"KDEN": {Id: "KDEN", Elevation: 5434, Location: math.Point2LL{-104.67, 39.86}},
		},
	}
	t.Cleanup(func() { db.DB = oldDB })

	airports := []av.ICAOAirportCode{"KTRM", "KMSY", "KDEN"}
	var e util.ErrorLogger
	regions := makeCircleAirportFilters("NOCA", "CONFLICT SUPPRESS", 5, 3000, airports, &e)
	if e.HaveErrors() {
		t.Fatal(e.String())
	}

	for _, icao := range airports {
		ap := db.DB.Airports[icao]
		if !regions.Inside(ap.Location, ap.Elevation) {
			t.Errorf("%s: aircraft on the ground at %d' is not inside the airport's filter region",
				icao, ap.Elevation)
		}
	}
}

// A location written in a scenario or facility configuration may name a fix as
// well as give a latitude-longitude, and which fixes exist isn't known until
// the scenario is finalized. So the JSON-facing field holds text until then: a
// math.Point2LL that JSON writes into directly can only take the lat-long
// spellings, which silently drops the names the documentation offers.
func TestLocationFieldsAreTextUntilFinalized(t *testing.T) {
	// Fields that hold an already-resolved location. They have a JSON name
	// because the sim is serialized to clients and into the config file, not
	// because anyone writes them by hand.
	resolved := map[string]bool{
		"aviation.Waypoint.Location": true,
	}

	pointType := reflect.TypeFor[math.Point2LL]()

	// bottom gives the type a field holds once slices, arrays, maps and
	// pointers are peeled away, stopping at Point2LL since it is itself an
	// array.
	var bottom func(t reflect.Type) reflect.Type
	bottom = func(t reflect.Type) reflect.Type {
		if t == pointType {
			return t
		}
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			return bottom(t.Elem())
		}
		return t
	}

	var found []string
	seen := make(map[reflect.Type]bool)

	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		if t = bottom(t); t.Kind() != reflect.Struct || t == pointType || seen[t] {
			return
		}
		seen[t] = true

		for f := range t.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if f.PkgPath != "" || name == "-" { // JSON can't write it
				continue
			}
			if bottom(f.Type) == pointType {
				// An untagged field is written only under its Go name, which
				// is the serialized form rather than anything hand-written.
				if name == "" {
					continue
				}
				pkg := t.PkgPath()
				if i := strings.LastIndex(pkg, "/"); i != -1 {
					pkg = pkg[i+1:]
				}
				found = append(found, pkg+"."+t.Name()+"."+f.Name)
				continue
			}
			walk(f.Type)
		}
	}

	walk(reflect.TypeFor[Group]())
	walk(reflect.TypeFor[sim.FacilityConfig]())

	for _, f := range found {
		if !resolved[f] {
			t.Errorf("%s is a math.Point2LL that JSON writes into; it should be an "+
				"av.ScenarioPoint2LL resolved when the scenario is finalized", f)
		}
	}
}

// scenario/scenario_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scenario

import (
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/enroute"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/traffic"
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

// A misspelled fix is offered the similarly-spelled ones near the facility, not
// the ones across the country.
func TestSimilarStaysNearTheFacility(t *testing.T) {
	oldDB := db.DB
	db.DB = &db.StaticDatabase{
		Fixes: map[string]db.Fix{
			"MERIT": {Id: "MERIT", Location: math.Point2LL{-73.13, 41.38}},  // ~50nm from JFK
			"MERIK": {Id: "MERIK", Location: math.Point2LL{-118.41, 33.94}}, // Los Angeles
			"ABCDE": {Id: "ABCDE", Location: math.Point2LL{-118.41, 33.94}},
			"ABXDE": {Id: "ABXDE", Location: math.Point2LL{-73.13, 41.38}},
		},
	}
	t.Cleanup(func() { db.DB = oldDB })

	sg := &Group{TRACON: "N90", Fixes: map[string]math.Point2LL{"MERIX": {-73.9, 40.8}}}
	sg.FacilityConfig.FacilityAdaptation.Center.Point2LL = math.Point2LL{-73.78, 40.64}

	if got := sg.Similar("MERIZ"); !slices.Equal(got, []string{"MERIT", "MERIX"}) {
		t.Errorf("expected the nearby MERIT and scenario fix MERIX, got %v", got)
	}
	// The one-edit match is across the country, so the nearby two-edit one
	// is offered instead.
	if got := sg.Similar("ABCDF"); !slices.Equal(got, []string{"ABXDE"}) {
		t.Errorf("expected the nearby ABXDE, got %v", got)
	}
}

// TestLocateFRDFromRedefinedFix checks that an FRD off a fix that the scenario
// group redefines is measured from the group's definition and not from the
// published fix of the same name: the Academy's BRITT is near Tulsa, while the
// published one is in Indiana.
func TestLocateFRDFromRedefinedFix(t *testing.T) {
	db.InitDB()
	britt := math.Point2LL{-95.612015, 36.349606}
	sg := &Group{Fixes: map[string]math.Point2LL{"BRITT": britt}}

	if p, ok := sg.Locate("BRITT090020"); !ok {
		t.Error("BRITT090020 doesn't locate")
	} else if d := math.NMDistance2LL(britt, p); math.Abs(d-20) > 0.2 {
		t.Errorf("BRITT090020 is %.1fnm from the group's BRITT", d)
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

// TestListedPositions verifies that the scenario's "listed_positions" beats
// its configuration's, which beats the facility's, that only the winning one
// is used, and that its positions are resolved to canonical TCPs.
func TestListedPositions(t *testing.T) {
	sg := &Group{
		FacilityConfig: sim.FacilityConfig{
			ControlPositions: map[sim.TCP]*av.Controller{"4J": {}, "4T": {}, "5E": {}, "N902K": {}},
			HandoffIDs:       []sim.HandoffID{{ID: "N90", StarsID: "N90", SingleCharStarsID: "N"}},
			FacilityAdaptation: sim.FacilityAdaptation{
				ListedPositions: map[sim.TCP]string{"4J": "tower"},
			},
		},
	}

	for _, tc := range []struct {
		name     string
		scenario map[sim.TCP]string
		config   map[sim.TCP]string
		want     map[sim.TCP]string
		wantErr  string
	}{
		{"scenario wins", map[sim.TCP]string{"4T": ""}, map[sim.TCP]string{"5E": "departures"}, map[sim.TCP]string{"4T": ""}, ""},
		{"configuration next", map[sim.TCP]string{}, map[sim.TCP]string{"5E": "departures"}, map[sim.TCP]string{"5E": "departures"}, ""},
		{"then the facility", nil, nil, map[sim.TCP]string{"4J": "tower"}, ""},
		{"short prefix resolves", map[sim.TCP]string{"N2K": "arrivals"}, nil, map[sim.TCP]string{"N902K": "arrivals"}, ""},
		{"unknown position", nil, map[sim.TCP]string{"5E": "", "9Z": ""}, map[sim.TCP]string{"5E": ""}, `configuration "FIN"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Scenario{ConfigurationString: "FIN", ListedPositions: tc.scenario}
			var e util.ErrorLogger
			got := s.listedPositions(sg, &sim.FacilityConfiguration{ListedPositions: tc.config}, &e)
			if !maps.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
			errs := slices.Collect(e.Errors())
			if tc.wantErr == "" && len(errs) > 0 {
				t.Errorf("unexpected errors: %v", errs)
			} else if tc.wantErr != "" && (len(errs) != 1 || !strings.Contains(errs[0], tc.wantErr) || !strings.Contains(errs[0], "9Z")) {
				t.Errorf("errors %v, want one naming 9Z and %s", errs, tc.wantErr)
			}
		})
	}

	sg.FacilityConfig.FacilityAdaptation.ListedPositions = nil
	var e util.ErrorLogger
	if got := (&Scenario{}).listedPositions(sg, &sim.FacilityConfiguration{}, &e); got != nil {
		t.Errorf("got %v with no listed_positions anywhere, want nil", got)
	}
}

// A group that names its timetables is offered just those, and a timetable a
// group names is offered to no other group.
func TestAttachTimetables(t *testing.T) {
	timetables := traffic.TimetableCatalog{Timetables: []traffic.Timetable{
		{ID: "summer_weekday", Airport: "KMSP"},
		{ID: "MSP Circa Summer 2005", Airport: "KMSP"},
		{ID: "evening", Airport: "KSTP"},
	}}
	airports := map[av.ICAOAirportCode]*av.Airport{"KMSP": {}, "KSTP": {}}
	attach := func(named map[av.ICAOAirportCode][]string) (map[string][]string, *util.ErrorLogger) {
		groups := map[string]map[string]*Group{"M98": {
			"Modern": {Airports: airports},
			"2005":   {Airports: airports, Timetables: named},
		}}
		catalogs := make(map[string]map[string]*Catalog)
		catalogs["M98"] = make(map[string]*Catalog)
		for name := range groups["M98"] {
			catalogs["M98"][name] = &Catalog{Scenarios: map[string]*Spec{"S": {
				ArrivalRunways: []sim.ArrivalRunway{{Airport: "KMSP"}, {Airport: "KSTP"}}}}}
		}
		var e util.ErrorLogger
		attachTimetables(catalogs, groups, timetables, &e)
		offered := make(map[string][]string)
		for name, catalog := range catalogs["M98"] {
			offered[name] = util.MapSlice(catalog.Scenarios["S"].Timetables,
				func(s traffic.TimetableSummary) string { return string(s.Airport) + "/" + s.ID })
		}
		return offered, &e
	}

	offered, e := attach(map[av.ICAOAirportCode][]string{"KMSP": {"MSP Circa Summer 2005"}})
	if e.HaveErrors() {
		t.Errorf("unexpected errors: %s", e.String())
	}
	if want := []string{"KMSP/summer_weekday", "KSTP/evening"}; !slices.Equal(offered["Modern"], want) {
		t.Errorf("modern group offered %v, want %v", offered["Modern"], want)
	}
	if want := []string{"KMSP/MSP Circa Summer 2005"}; !slices.Equal(offered["2005"], want) {
		t.Errorf("2005 group offered %v, want %v", offered["2005"], want)
	}

	_, e = attach(map[av.ICAOAirportCode][]string{"KMSP": {"MSP 2005"}, "KORD": {"summer_weekday"}})
	for _, want := range []string{`"MSP 2005", which isn't a timetable for KMSP`,
		"KORD, which isn't one of the group's airports"} {
		if !strings.Contains(e.String(), want) {
			t.Errorf("errors %q don't report %q", e.String(), want)
		}
	}
}

// A polygon written longitude first lands thousands of miles from the
// facility and is reported; a significant point may name a far-off
// destination and is not.
func TestCheckLocationsNearFacility(t *testing.T) {
	sg := &Group{Name: "test", TRACON: "BHM"}
	fa := &sg.FacilityConfig.FacilityAdaptation
	fa.Center.Point2LL = math.Point2LL{-86.75, 33.56}
	fa.MaxDistance = 200
	fa.Filters.InhibitCA = sim.FilterRegions{{AirspaceVolume: av.AirspaceVolume{
		Type: av.AirspaceVolumePolygon,
		Vertices: []math.Point2LL{
			{-86.89, 33.53}, {-86.64, 33.67}, {-86.58, 33.61}, // correct
			{33.46, -86.83}, // longitude first
		},
	}}}
	fa.SignificantPoints = map[string]sim.SignificantPoint{
		"DVV": {Location: av.ScenarioPoint2LL{Point2LL: math.Point2LL{-104.62, 39.89}}},
	}

	var e util.ErrorLogger
	CheckLocations(sg, &e)
	errs := e.String()
	if !strings.Contains(errs, "InhibitCA[0].AirspaceVolume.Vertices[3]") {
		t.Errorf("expected the longitude-first vertex to be reported, got %q", errs)
	}
	if strings.Contains(errs, "Vertices[0]") || strings.Contains(errs, "SignificantPoints") {
		t.Errorf("unexpected errors: %q", errs)
	}
}

// A location written as a name that resolves far from the facility is likely a
// misspelling, so the error offers the similarly-spelled fixes nearby.
func TestCheckLocationsSuggestsNearbyFixes(t *testing.T) {
	far := math.Point2LL{-81.36, 28.80}
	oldDB := db.DB
	db.DB = &db.StaticDatabase{
		Fixes: map[string]db.Fix{
			"PAOLA": {Id: "PAOLA", Location: far},
			"PAOLE": {Id: "PAOLE", Location: math.Point2LL{-94.8, 38.6}},
		},
	}
	t.Cleanup(func() { db.DB = oldDB })

	sg := &Group{Name: "test", TRACON: "MCI"}
	fa := &sg.FacilityConfig.FacilityAdaptation
	fa.Center.Point2LL = math.Point2LL{-94.71, 39.30}
	fa.MaxDistance = 200
	fa.RestrictionAreas = map[int]av.RestrictionArea{
		1: {TextPosition: av.ScenarioPoint2LL{String: "PAOLA", Point2LL: far}},
	}

	var e util.ErrorLogger
	CheckLocations(sg, &e)
	if errs := e.String(); !strings.Contains(errs, `"PAOLA"`) || !strings.Contains(errs, "Did you mean: PAOLE") {
		t.Errorf("expected PAOLA to be reported with PAOLE suggested, got %q", errs)
	}
}

// TestLoadMissingVideoMapOverride checks that an override video map that
// can't be read is reported with the other override errors and skipped, as
// the other override files are, rather than keeping the built-in scenarios
// from loading.
func TestLoadMissingVideoMapOverride(t *testing.T) {
	db.InitDB()
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	missing := filepath.Join(t.TempDir(), "missing.mappack")
	var e util.ErrorLogger
	tables, overrideErrors := Load(OverrideFiles{VideoMap: missing}, &e, lg)
	if e.HaveErrors() {
		t.Fatalf("unexpected errors: %s", e.String())
	}
	if tables == nil || len(tables.Groups) == 0 {
		t.Error("the built-in scenarios didn't load")
	}
	if !strings.Contains(overrideErrors, missing) {
		t.Errorf("override errors %q don't mention %s", overrideErrors, missing)
	}
}

// TestLoadRereadsFacilityConfigs checks that every Load reads the facility
// configurations afresh, so that a reload picks up an override that was added
// or removed since the last one just as it does an edited file.
func TestLoadRereadsFacilityConfigs(t *testing.T) {
	db.InitDB()
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	installed, err := fs.ReadFile(util.GetResourcesFS(), "configurations/ZNY/ABE.json")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(installed, &config); err != nil {
		t.Fatal(err)
	}
	config["disable_tfr_restriction_areas"] = json.RawMessage("true")
	contents, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	override := filepath.Join(t.TempDir(), "ABE.json")
	if err := os.WriteFile(override, contents, 0o644); err != nil {
		t.Fatal(err)
	}

	tfrAreasDisabled := func(overrides OverrideFiles) bool {
		var e util.ErrorLogger
		tables, overrideErrors := Load(overrides, &e, lg)
		if e.HaveErrors() || overrideErrors != "" {
			t.Fatalf("unexpected errors: %s%s", e.String(), overrideErrors)
		}
		for _, sg := range tables.Groups["ABE"] {
			return sg.FacilityConfig.DisableTFRRestrictionAreas
		}
		t.Fatal("no ABE scenario groups")
		return false
	}

	if tfrAreasDisabled(OverrideFiles{}) {
		t.Fatal("the installed ABE configuration already disables TFR restriction areas")
	}
	if !tfrAreasDisabled(OverrideFiles{FacilityConfigs: []string{override}}) {
		t.Error("an override added after a load wasn't used")
	}
	if tfrAreasDisabled(OverrideFiles{}) {
		t.Error("an override was still used after it was removed")
	}
}

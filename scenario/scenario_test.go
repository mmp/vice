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

// A departure waiting to take off sits at the end of the runway, short of a
// displaced threshold; the airport's polygon regions still have to cover it.
func TestAirportPolygonFiltersCoverRunwayEnds(t *testing.T) {
	const nmPerLongitude = 46
	pt := func(x, y float32) math.Point2LL { return math.NM2LL([2]float32{x, y}, nmPerLongitude) }
	rwy := func(id string, threshold, start math.Point2LL) av.Runway {
		return av.Runway{Id: id, Threshold: threshold, Start: start}
	}

	oldDB := db.DB
	db.DB = &db.StaticDatabase{
		Airports: map[av.ICAOAirportCode]db.Airport{
			"KONE": {Id: "KONE", Elevation: 20, Location: pt(1, 0), Runways: []av.Runway{
				rwy("9", pt(0.5, 0), pt(0, 0)),
				rwy("27", pt(2, 0), pt(2, 0)),
			}},
			"KTWO": {Id: "KTWO", Elevation: 20, Location: pt(1, 0), Runways: []av.Runway{
				rwy("9", pt(0.4, 0), pt(0, 0)),
				rwy("27", pt(2, 0), pt(2, 0)),
				rwy("18", pt(1, 1), pt(1, 1)),
				rwy("36", pt(1, -1), pt(1, -1)),
			}},
		},
	}
	t.Cleanup(func() { db.DB = oldDB })

	airports := []av.ICAOAirportCode{"KONE", "KTWO"}
	var e util.ErrorLogger
	regions := makePolygonAirportFilters("SURF", "SURFACE TRACKING", 0.15, 200, airports, nmPerLongitude, &e)
	if e.HaveErrors() {
		t.Fatal(e.String())
	}

	for _, icao := range airports {
		ap := db.DB.Airports[icao]
		for _, r := range ap.Runways {
			if !regions.Inside(r.Start, ap.Elevation) {
				t.Errorf("%s: a departure waiting on runway %s is not inside the airport's filter region", icao, r.Id)
			}
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

// TestCheckRouteRevisions checks that the procedure names a scenario writes
// out itself are reported when they carry a revision the CIFP no longer
// charts, and left alone when current or when nothing of their name is
// charted, as with an NRS waypoint that starts like a procedure name.
func TestCheckRouteRevisions(t *testing.T) {
	oldDB := db.DB
	db.DB = &db.StaticDatabase{
		Airports: map[av.ICAOAirportCode]db.Airport{
			"KXYZ": {SIDs: map[string]av.SID{"CLIMB5": {}}, STARs: map[string]av.STAR{"DSCND3": {}}},
		},
	}
	t.Cleanup(func() { db.DB = oldDB })

	sg := &Group{
		Airports: map[av.ICAOAirportCode]*av.Airport{"KXYZ": {
			DepartureRoutes: map[av.RunwayID]map[av.ExitID]av.ExitRoutes{
				"27": {"CLIMB": {{SID: "CLIMB4"}}, "OTHER": {{SID: "CLIMB5"}, {SID: "VECTR2"}}},
			},
			Departures: []av.Departure{
				{Destination: "KABC", WaypointsString: "CLIMB4 CLIMB J60 KD66Y"},
				{Destination: "KABC", WaypointsString: "CLIMB5 CLIMB J60"},
			},
		}},
		InboundFlows: map[string]*av.InboundFlow{
			"EAST": {Arrivals: []av.Arrival{
				{Airports: []av.ICAOAirportCode{"KXYZ"}, FlightStripDisplayRoute: "ABC DSCND2"},
				{Airports: []av.ICAOAirportCode{"KXYZ"}, FlightStripDisplayRoute: "ABC DSCND3"},
			}},
		},
	}

	var e util.ErrorLogger
	checkRouteRevisions(sg, &e)
	errs := strings.Split(strings.TrimSpace(e.String()), "\n")
	want := []string{"CLIMB4 isn't a SID", "CLIMB4 isn't a SID", "DSCND2 isn't a STAR"}
	if len(errs) != len(want) {
		t.Fatalf("got %d errors, want %d:\n%s", len(errs), len(want), e.String())
	}
	for i, w := range want {
		if !strings.Contains(errs[i], w) {
			t.Errorf("error %d: %q doesn't report %q", i, errs[i], w)
		}
	}
}

// TestCheckDepartureAssignments checks that a departure_assignments entry
// is reported when no departure or go-around can look it up: one naming an
// exit rather than a SID, a SID no route flies, or a SID whose departures
// start with a virtual controller in every scenario.
func TestCheckDepartureAssignments(t *testing.T) {
	oldDB := db.DB
	db.DB = &db.StaticDatabase{
		Airports: map[av.ICAOAirportCode]db.Airport{
			"KXYZ": {Runways: []av.Runway{{Id: "9"}, {Id: "27"}}},
			"KABC": {Runways: []av.Runway{{Id: "4"}}},
		},
	}
	t.Cleanup(func() { db.DB = oldDB })

	sg := &Group{
		Airports: map[av.ICAOAirportCode]*av.Airport{
			"KXYZ": {DepartureRoutes: map[av.RunwayID]map[av.ExitID]av.ExitRoutes{
				"27_SOUTH": {
					"CLIMB": {{SID: "CLIMB5"}},
					"TOWER": {{SID: "VECTR2", DepartureController: "1T"}},
				},
			}},
			"KABC": {DepartureController: "1T", DepartureRoutes: map[av.RunwayID]map[av.ExitID]av.ExitRoutes{
				"4": {"CLIMB": {{SID: "CLIMB5"}}},
			}},
		},
		Scenarios: map[string]*Scenario{"S": {ControllerConfiguration: sim.ControllerConfiguration{
			DefaultConsolidation: sim.PositionConsolidation{"1D": nil},
		}}},
	}
	sg.FacilityConfig.ControlPositions = map[sim.TCP]*av.Controller{"1D": {}, "1T": {}}
	sg.FacilityConfig.FacilityAdaptation.Configurations = map[string]*sim.FacilityConfiguration{
		"C": {DepartureAssignments: map[string]sim.TCP{
			"KXYZ":          "1D", // go-arounds and departures
			"KXYZ/CLIMB5":   "1D",
			"KXYZ/27_SOUTH": "1D", // the departure runway as scenarios name it
			"KXYZ/9":        "1D", // go-arounds land on it
			"KXYZ/CLIMB":    "1D", // an exit, not a SID
			"KXYZ/QUAKE2":   "1D", // flown nowhere
			"KXYZ/VECTR2":   "1D", // its departures start with the virtual 1T
			"KABC/CLIMB5":   "1D", // so do KABC's
			"KJKL":          "1D", // not one of the facility's airports
		}},
	}

	var e util.ErrorLogger
	checkDepartureAssignments(map[string]map[string]*Group{"TST": {"G": sg}}, &e)
	var got []string
	for line := range strings.SplitSeq(strings.TrimSpace(e.String()), "\n") {
		if _, spec, ok := strings.Cut(line, "departure_assignments: "); ok {
			spec, _, _ = strings.Cut(spec, " matches")
			got = append(got, spec)
		}
	}
	want := []string{`"KABC/CLIMB5"`, `"KJKL"`, `"KXYZ/CLIMB"`, `"KXYZ/QUAKE2"`, `"KXYZ/VECTR2"`}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v\n%s", got, want, e.String())
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

// TestLoadRereadsFacilityConfigs checks that every Load reads the facility
// configurations afresh, so that a reload picks up a file that was added to
// or removed from the overlay directory since the last one just as it does
// an edited file. The overlay's file is named in lowercase to check that it
// still replaces configurations/ZNY/ABE.json.
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
	overlay := t.TempDir()
	if err := os.MkdirAll(filepath.Join(overlay, "configurations", "zny"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overlay, "configurations", "zny", "abe.json"), contents, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { util.SetResourcesOverlay("") })

	tfrAreasDisabled := func(overlayDir string) bool {
		util.SetResourcesOverlay(overlayDir)
		var e util.ErrorLogger
		tables := Load(&e, lg)
		if e.HaveErrors() {
			t.Fatalf("unexpected errors: %s", e.String())
		}
		for _, sg := range tables.Groups["ABE"] {
			return sg.FacilityConfig.DisableTFRRestrictionAreas
		}
		t.Fatal("no ABE scenario groups")
		return false
	}

	if tfrAreasDisabled("") {
		t.Fatal("the installed ABE configuration already disables TFR restriction areas")
	}
	if !tfrAreasDisabled(overlay) {
		t.Error("an overlay set after a load wasn't used")
	}
	if unread, err := util.UnreadOverlayFiles(); err != nil || len(unread) > 0 {
		t.Errorf("the load didn't read %v from the overlay (%v)", unread, err)
	}
	if tfrAreasDisabled("") {
		t.Error("an overlay was still used after it was removed")
	}
}

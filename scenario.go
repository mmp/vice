// scenario.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type ScenarioGroup struct {
	Name             string                 `json:"name"`
	Airports         map[string]*Airport    `json:"airports"`
	VideoMapFile     string                 `json:"video_map_file"`
	Fixes            map[string]Point2LL    `json:"-"`
	FixesStrings     map[string]string      `json:"fixes"`
	Scenarios        map[string]*Scenario   `json:"scenarios"`
	DefaultScenario  string                 `json:"default_scenario"`
	ControlPositions map[string]*Controller `json:"control_positions"`
	Scratchpads      map[string]string      `json:"scratchpads"`
	Airspace         Airspace               `json:"airspace"`
	ArrivalGroups    map[string][]Arrival   `json:"arrival_groups"`

	Center         Point2LL              `json:"-"`
	CenterString   string                `json:"center"`
	Range          float32               `json:"range"`
	PrimaryAirport string                `json:"primary_airport"`
	RadarSites     map[string]*RadarSite `json:"radar_sites"`
	STARSMaps      []STARSMap            `json:"stars_maps"`

	NmPerLatitude     float32 `json:"nm_per_latitude"`
	NmPerLongitude    float32 `json:"nm_per_longitude"`
	MagneticVariation float32 `json:"magnetic_variation"`
}

type Arrival struct {
	Waypoints       WaypointArray            `json:"waypoints"`
	RunwayWaypoints map[string]WaypointArray `json:"runway_waypoints"`
	CruiseAltitude  float32                  `json:"cruise_altitude"`
	Route           string                   `json:"route"`

	InitialController string  `json:"initial_controller"`
	InitialAltitude   float32 `json:"initial_altitude"`
	ClearedAltitude   float32 `json:"cleared_altitude"`
	InitialSpeed      float32 `json:"initial_speed"`
	SpeedRestriction  float32 `json:"speed_restriction"`
	ExpectApproach    string  `json:"expect_approach"`
	Scratchpad        string  `json:"scratchpad"`

	Airlines map[string][]ArrivalAirline `json:"airlines"`
}

type ArrivalAirline struct {
	ICAO    string `json:"icao"`
	Airport string `json:"airport"`
	Fleet   string `json:"fleet,omitempty"`
}

type Airspace struct {
	Boundaries map[string][]Point2LL       `json:"boundaries"`
	Volumes    map[string][]AirspaceVolume `json:"volumes"`
}

type AirspaceVolume struct {
	LowerLimit    int          `json:"lower"`
	UpperLimit    int          `json:"upper"`
	Boundaries    [][]Point2LL `json:"-"`
	BoundaryNames []string     `json:"boundaries"`
}

type Scenario struct {
	SoloController     string                          `json:"solo_controller"`
	MultiControllers   map[string]*MultiUserController `json:"multi_controllers"`
	Wind               Wind                            `json:"wind"`
	VirtualControllers []string                        `json:"controllers"`

	// Map from arrival group name to map from airport name to default rate...
	ArrivalGroupDefaultRates map[string]map[string]int `json:"arrivals"`

	ApproachAirspace       []AirspaceVolume `json:"-"`
	DepartureAirspace      []AirspaceVolume `json:"-"`
	ApproachAirspaceNames  []string         `json:"approach_airspace"`
	DepartureAirspaceNames []string         `json:"departure_airspace"`

	DepartureRunways []ScenarioGroupDepartureRunway `json:"departure_runways,omitempty"`
	ArrivalRunways   []ScenarioGroupArrivalRunway   `json:"arrival_runways,omitempty"`

	DefaultMap string `json:"default_map"`
}

type MultiUserController struct {
	Primary          bool     `json:"primary"`
	Departure        bool     `json:"departure"`
	BackupController string   `json:"backup"`
	Arrivals         []string `json:"arrivals"`
}

type ScenarioGroupDepartureRunway struct {
	Airport     string `json:"airport"`
	Runway      string `json:"runway"`
	Category    string `json:"category,omitempty"`
	DefaultRate int    `json:"rate"`

	ExitRoutes map[string]ExitRoute // copied from airport's  departure_routes
}

type ScenarioGroupArrivalRunway struct {
	Airport string `json:"airport"`
	Runway  string `json:"runway"`
}

func (s *Scenario) PostDeserialize(sg *ScenarioGroup, e *ErrorLogger) {
	for _, as := range s.ApproachAirspaceNames {
		if vol, ok := sg.Airspace.Volumes[as]; !ok {
			e.ErrorString("unknown approach airspace \"%s\"", as)
		} else {
			s.ApproachAirspace = append(s.ApproachAirspace, vol...)
		}
	}
	for _, as := range s.DepartureAirspaceNames {
		if vol, ok := sg.Airspace.Volumes[as]; !ok {
			e.ErrorString("unknown departure airspace \"%s\"", as)
		} else {
			s.DepartureAirspace = append(s.DepartureAirspace, vol...)
		}
	}

	sort.Slice(s.DepartureRunways, func(i, j int) bool {
		if s.DepartureRunways[i].Airport != s.DepartureRunways[j].Airport {
			return s.DepartureRunways[i].Airport < s.DepartureRunways[j].Airport
		} else if s.DepartureRunways[i].Runway != s.DepartureRunways[j].Runway {
			return s.DepartureRunways[i].Runway < s.DepartureRunways[j].Runway
		} else {
			return s.DepartureRunways[i].Category < s.DepartureRunways[j].Category
		}
	})

	for i, rwy := range s.DepartureRunways {
		e.Push("Departure runway " + rwy.Airport + " " + rwy.Runway)
		if ap, ok := sg.Airports[rwy.Airport]; !ok {
			e.ErrorString("airport not found")
		} else {
			if routes, ok := ap.DepartureRoutes[rwy.Runway]; !ok {
				e.ErrorString("runway departure routes not found")
			} else {
				s.DepartureRunways[i].ExitRoutes = routes
			}

			if rwy.Category != "" {
				found := false
				for _, dep := range ap.Departures {
					if ap.ExitCategories[dep.Exit] == rwy.Category {
						found = true
						break
					}
				}
				if !found {
					e.ErrorString("no departures have exit category \"%s\"", rwy.Category)
				}
			}
		}
		e.Pop()
	}

	sort.Slice(s.ArrivalRunways, func(i, j int) bool {
		if s.ArrivalRunways[i].Airport == s.ArrivalRunways[j].Airport {
			return s.ArrivalRunways[i].Runway < s.ArrivalRunways[j].Runway
		}
		return s.ArrivalRunways[i].Airport < s.ArrivalRunways[j].Airport
	})

	for _, rwy := range s.ArrivalRunways {
		e.Push("Arrival runway " + rwy.Airport + " " + rwy.Runway)
		if ap, ok := sg.Airports[rwy.Airport]; !ok {
			e.ErrorString("airport not found")
		} else {
			found := false
			for _, appr := range ap.Approaches {
				if appr.Runway == rwy.Runway {
					found = true
					break
				}
			}

			if !found {
				e.ErrorString("no approach found that reaches this runway")
			}
		}
		e.Pop()
	}

	// These shouldn't be listed in "controllers"; just silently remove
	// them if they're there.
	s.VirtualControllers = FilterSlice(s.VirtualControllers, func(c string) bool {
		_, ok := s.MultiControllers[c]
		return !ok && c != s.SoloController
	})

	if _, ok := sg.ControlPositions[s.SoloController]; s.SoloController != "" && !ok {
		e.ErrorString("controller \"%s\" for \"solo_controller\" is unknown", s.SoloController)
	}

	// Various multi_controllers validations
	primaryController := ""
	departureController := ""
	for callsign, mc := range s.MultiControllers {
		e.Push("\"multi_controllers\": " + callsign)
		if mc.Primary {
			if primaryController != "" {
				e.ErrorString("multiple controllers specified as \"primary\": %s %s",
					primaryController, callsign)
			} else {
				primaryController = callsign
			}
		}
		if mc.Departure {
			if departureController != "" {
				e.ErrorString("multiple controllers specified as \"departure\": %s %s",
					departureController, callsign)
			} else {
				departureController = callsign
			}
		}

		// Make sure all arrivals are valid. Below we make sure all
		// included arrivals have a controller.
		for _, arr := range mc.Arrivals {
			if _, ok := s.ArrivalGroupDefaultRates[arr]; !ok {
				e.ErrorString("arrival \"%s\" not found in scenario", arr)
			}
		}

		e.Pop()
	}
	if len(s.MultiControllers) > 0 && primaryController == "" {
		e.ErrorString("No controller in \"multi_controllers\" was specified as \"primary\"")
	}
	if len(s.MultiControllers) > 0 && departureController == "" {
		e.ErrorString("No controller in \"multi_controllers\" was specified as \"departure\"")
	}

	havePathToPrimary := make(map[string]interface{})
	havePathToPrimary[primaryController] = nil
	var followPathToPrimary func(callsign string, mc *MultiUserController, depth int) bool
	followPathToPrimary = func(callsign string, mc *MultiUserController, depth int) bool {
		if callsign == "" {
			return false
		}
		if _, ok := havePathToPrimary[callsign]; ok {
			return true
		}
		if depth == 0 || mc.BackupController == "" {
			return false
		}

		bmc, ok := s.MultiControllers[mc.BackupController]
		if !ok {
			e.ErrorString("Backup controller \"%s\" for \"%s\" is unknown",
				mc.BackupController, callsign)
			return false
		}

		if followPathToPrimary(mc.BackupController, bmc, depth-1) {
			havePathToPrimary[callsign] = nil
			return true
		}
		return false
	}
	for callsign, mc := range s.MultiControllers {
		followPathToPrimary(callsign, mc, 25)
	}

	for _, name := range SortedMapKeys(s.ArrivalGroupDefaultRates) {
		e.Push("Arrival group " + name)
		// Make sure the arrival group has been defined
		if arrivals, ok := sg.ArrivalGroups[name]; !ok {
			e.ErrorString("arrival group not found")
		} else {
			// Check the airports in it
			for airport := range s.ArrivalGroupDefaultRates[name] {
				e.Push("Airport " + airport)
				if _, ok := sg.Airports[airport]; !ok {
					e.ErrorString("unknown arrival airport")
				} else {
					found := false
					for _, ar := range arrivals {
						if _, ok := ar.Airlines[airport]; ok {
							found = true
							break
						}
					}
					if !found {
						e.ErrorString("airport not used for any arrivals")
					}
				}
				e.Pop()
			}

			// For multi-controller, sure some controller covers the
			// arrival group.
			if len(s.MultiControllers) > 0 {
				count := 0
				for _, mc := range s.MultiControllers {
					if idx := Find(mc.Arrivals, name); idx != -1 {
						count++
					}
				}
				if count == 0 {
					e.ErrorString("no controller in \"multi_controllers\" has this arrival group in their \"arrivals\"")
				} else if count > 1 {
					e.ErrorString("more than one controller in \"multi_controllers\" has this arrival group in their \"arrivals\"")
				}
			}
		}
		e.Pop()
	}

	for _, ctrl := range s.VirtualControllers {
		if _, ok := sg.ControlPositions[ctrl]; !ok {
			e.ErrorString("controller \"%s\" unknown", ctrl)
		}
	}

	if s.DefaultMap == "" {
		e.ErrorString("must specify a default video map using \"default_map\"")
	} else {
		idx := FindIf(sg.STARSMaps, func(m STARSMap) bool { return m.Name == s.DefaultMap })
		if idx == -1 {
			e.ErrorString("video map \"%s\" not found in \"stars_maps\"", s.DefaultMap)
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// ScenarioGroup

func (sg *ScenarioGroup) locate(s string) (Point2LL, bool) {
	s = strings.ToUpper(s)
	// ScenarioGroup's definitions take precedence...
	if ap, ok := sg.Airports[s]; ok {
		return ap.Location, true
	} else if p, ok := sg.Fixes[s]; ok {
		return p, true
	} else if n, ok := database.Navaids[strings.ToUpper(s)]; ok {
		return n.Location, ok
	} else if ap, ok := database.Airports[strings.ToUpper(s)]; ok {
		return ap.Location, ok
	} else if f, ok := database.Fixes[strings.ToUpper(s)]; ok {
		return f.Location, ok
	} else if p, err := ParseLatLong([]byte(s)); err == nil {
		return p, true
	} else {
		return Point2LL{}, false
	}
}

func (sg *ScenarioGroup) PostDeserialize(e *ErrorLogger, simConfigurations map[string]*SimConfiguration) {
	// Do these first!
	sg.Fixes = make(map[string]Point2LL)
	for fix, latlong := range sg.FixesStrings {
		fix := strings.ToUpper(fix)
		if pos, ok := sg.locate(latlong); !ok {
			e.ErrorString("unknown location \"%s\" specified for fix \"%s\"", latlong, fix)
		} else if _, ok := sg.Fixes[fix]; ok {
			e.ErrorString("fix \"%s\" has multiple definitions", fix)
		} else {
			sg.Fixes[fix] = pos
		}
	}

	for name, volumes := range sg.Airspace.Volumes {
		for i, vol := range volumes {
			e.Push("Airspace volume " + name)
			for _, b := range vol.BoundaryNames {
				if pts, ok := sg.Airspace.Boundaries[b]; !ok {
					e.ErrorString("airspace boundary \"%s\" not found", b)
				} else {
					sg.Airspace.Volumes[name][i].Boundaries = append(sg.Airspace.Volumes[name][i].Boundaries, pts)
				}
			}
			e.Pop()
		}
	}

	for name, ap := range sg.Airports {
		e.Push("Airport " + name)
		ap.PostDeserialize(sg, e)
		e.Pop()
	}

	if sg.PrimaryAirport == "" {
		e.ErrorString("\"primary_airport\" not specified")
	} else if _, ok := sg.locate(sg.PrimaryAirport); !ok {
		e.ErrorString("\"primary_airport\" \"%s\" unknown", sg.PrimaryAirport)
	}

	if sg.NmPerLatitude == 0 {
		e.ErrorString("\"nm_per_latitude\" not specified")
	}
	if sg.NmPerLongitude == 0 {
		e.ErrorString("\"nm_per_latitude\" not specified")
	}

	if _, ok := sg.Scenarios[sg.DefaultScenario]; !ok {
		e.ErrorString("default scenario \"%s\" not found in \"scenarios\"", sg.DefaultScenario)
	}

	for callsign, ctrl := range sg.ControlPositions {
		e.Push("Controller " + callsign)

		ctrl.Callsign = callsign

		if ctrl.Frequency < 118000 || ctrl.Frequency > 138000 {
			e.ErrorString("invalid frequency: %6.3f", float32(ctrl.Frequency)/1000)
		}
		if ctrl.SectorId == "" {
			e.ErrorString("no \"sector_id\" specified")
		}
		if ctrl.Scope == "" {
			e.ErrorString("no \"scope_char\" specified")
		}
		if ctrl.FullName == "" {
			e.ErrorString("no \"full_name\" specified")
		}
		e.Pop()
	}

	if sg.CenterString == "" {
		e.ErrorString("No \"center\" specified")
	} else if pos, ok := sg.locate(sg.CenterString); !ok {
		e.ErrorString("unknown location \"%s\" specified for \"center\"", sg.CenterString)
	} else {
		sg.Center = pos
	}

	if sg.Range == 0 {
		sg.Range = 50
	}

	if len(sg.RadarSites) == 0 {
		e.ErrorString("no \"radar_sites\" specified")
	}
	for name, rs := range sg.RadarSites {
		e.Push("Radar site " + name)
		if _, ok := sg.locate(rs.Position); rs.Position == "" || !ok {
			e.ErrorString("radar site position \"%s\" not found", rs.Position)
		}
		if rs.Char == "" {
			e.ErrorString("radar site is missing \"char\"")
		}
		e.Pop()
	}

	for name, arrivals := range sg.ArrivalGroups {
		e.Push("Arrival group " + name)
		if len(arrivals) == 0 {
			e.ErrorString("no arrivals in arrival group")
		}

		for _, ar := range arrivals {
			if ar.Route == "" {
				e.ErrorString("\"route\" not specified")
			}

			e.Push("Route " + ar.Route)

			if len(ar.Waypoints) < 2 {
				e.ErrorString("must provide at least two \"waypoints\" for approach " +
					"(even if \"runway_waypoints\" are provided)")
			} else {
				sg.InitializeWaypointLocations(ar.Waypoints, e)

				for rwy, wp := range ar.RunwayWaypoints {
					e.Push("Runway " + rwy)
					sg.InitializeWaypointLocations(wp, e)

					if wp[0].Fix != ar.Waypoints[len(ar.Waypoints)-1].Fix {
						e.ErrorString("initial \"runway_waypoints\" fix must match " +
							"last \"waypoints\" fix")
					}
					e.Pop()
				}
			}

			for arrivalAirport, airlines := range ar.Airlines {
				e.Push("Arrival airport " + arrivalAirport)
				for _, al := range airlines {
					database.CheckAirline(al.ICAO, al.Fleet, e)
				}
				e.Pop()
			}

			if ar.InitialController == "" {
				e.ErrorString("\"initial_controller\" missing")
			} else if _, ok := sg.ControlPositions[ar.InitialController]; !ok {
				e.ErrorString("controller \"%s\" not found for \"initial_controller\"", ar.InitialController)
			}

			e.Pop()
		}
		e.Pop()
	}

	// Do after airports!
	for name, s := range sg.Scenarios {
		e.Push("Scenario " + name)
		s.PostDeserialize(sg, e)
		e.Pop()
	}

	initializeSimConfigurations(sg, simConfigurations, *server == true)
}

func initializeSimConfigurations(sg *ScenarioGroup,
	simConfigurations map[string]*SimConfiguration, multiController bool) {
	config := &SimConfiguration{
		ScenarioConfigs:  make(map[string]*SimScenarioConfiguration),
		ControlPositions: sg.ControlPositions,
		DefaultScenario:  sg.DefaultScenario,
	}

	for name, scenario := range sg.Scenarios {
		sc := &SimScenarioConfiguration{
			DepartureChallenge: 0.25,
			GoAroundRate:       0.05,
			Wind:               scenario.Wind,
			ArrivalGroupRates:  scenario.ArrivalGroupDefaultRates,
			DepartureRunways:   scenario.DepartureRunways,
			ArrivalRunways:     scenario.ArrivalRunways,
		}

		if multiController {
			if len(scenario.MultiControllers) == 0 {
				// not a multi-controller scenario
				continue
			}
			sc.SelectedController, _ = GetPrimaryController(scenario.MultiControllers)
		} else {
			if scenario.SoloController == "" {
				// multi-controller only
				continue
			}
			sc.SelectedController = scenario.SoloController
		}

		sc.DepartureRates = make(map[string]map[string]map[string]int)
		for _, rwy := range scenario.DepartureRunways {
			if _, ok := sc.DepartureRates[rwy.Airport]; !ok {
				sc.DepartureRates[rwy.Airport] = make(map[string]map[string]int)
			}
			if _, ok := sc.DepartureRates[rwy.Airport][rwy.Runway]; !ok {
				sc.DepartureRates[rwy.Airport][rwy.Runway] = make(map[string]int)
			}
			sc.DepartureRates[rwy.Airport][rwy.Runway][rwy.Category] = rwy.DefaultRate
		}

		config.ScenarioConfigs[name] = sc
	}

	// Skip scenario groups that don't have any single/multi-controller
	// scenarios, as appropriate.
	if len(config.ScenarioConfigs) > 0 {
		simConfigurations[sg.Name] = config
	}
}

func (sg *ScenarioGroup) InitializeWaypointLocations(waypoints []Waypoint, e *ErrorLogger) {
	var prev Point2LL

	for i, wp := range waypoints {
		if e != nil {
			e.Push("Fix " + wp.Fix)
		}
		if pos, ok := sg.locate(wp.Fix); !ok {
			if e != nil {
				e.ErrorString("unable to locate waypoint")
			}
		} else {
			waypoints[i].Location = pos

			d := nmdistance2ll(prev, waypoints[i].Location)
			if i > 1 && d > 75 && e != nil {
				e.ErrorString("waypoint at %s is suspiciously far from previous one (%s at %s): %f nm",
					waypoints[i].Location.DDString(), waypoints[i-1].Fix, waypoints[i-1].Location.DDString(), d)
			}
			prev = waypoints[i].Location
		}
		if e != nil {
			e.Pop()
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// Airspace

func InAirspace(p Point2LL, alt float32, volumes []AirspaceVolume) (bool, [][2]int) {
	var altRanges [][2]int
	for _, v := range volumes {
		inside := false
		for _, pts := range v.Boundaries {
			if PointInPolygon(p, pts) {
				inside = !inside
			}
		}
		if inside {
			altRanges = append(altRanges, [2]int{v.LowerLimit, v.UpperLimit})
		}
	}

	// Sort altitude ranges and then merge ones that have 1000 foot separation
	sort.Slice(altRanges, func(i, j int) bool { return altRanges[i][0] < altRanges[j][0] })
	var mergedAlts [][2]int
	i := 0
	inside := false
	for i < len(altRanges) {
		low := altRanges[i][0]
		high := altRanges[i][1]

		for i+1 < len(altRanges) {
			if altRanges[i+1][0]-high <= 1000 {
				// merge
				high = altRanges[i+1][1]
				i++
			} else {
				break
			}
		}

		// 10 feet of slop for rounding error
		inside = inside || (int(alt)+10 >= low && int(alt)-10 <= high)

		mergedAlts = append(mergedAlts, [2]int{low, high})
		i++
	}

	return inside, mergedAlts
}

///////////////////////////////////////////////////////////////////////////
// LoadScenarioGroups

var (
	//go:embed scenarios/*.json
	embeddedScenarioGroups embed.FS

	//go:embed videomaps/*.json.zst
	embeddedVideoMaps embed.FS
)

func loadVideoMaps(filesystem fs.FS, path string, e *ErrorLogger) map[string]CommandBuffer {
	e.Push("File " + path)
	defer e.Pop()

	contents, err := fs.ReadFile(filesystem, path)
	if err != nil {
		e.Error(err)
		return nil
	}

	if strings.HasSuffix(strings.ToLower(path), ".zst") {
		contents = []byte(decompressZstd(string(contents)))
	}

	var maps map[string][]Point2LL
	if err := UnmarshalJSON(contents, &maps); err != nil {
		e.Error(err)
		return nil
	}

	vm := make(map[string]CommandBuffer)
	for name, segs := range maps {
		ld := GetLinesDrawBuilder()
		for i := 0; i < len(segs)/2; i++ {
			ld.AddLine(segs[2*i], segs[2*i+1])
		}
		var cb CommandBuffer
		ld.GenerateCommands(&cb)

		vm[name] = cb

		ReturnLinesDrawBuilder(ld)
	}

	return vm
}

func loadScenarioGroup(filesystem fs.FS, path string, e *ErrorLogger) *ScenarioGroup {
	e.Push("File " + path)
	defer e.Pop()

	contents, err := fs.ReadFile(filesystem, path)
	if err != nil {
		e.Error(err)
		return nil
	}

	CheckJSONVsSchema[ScenarioGroup](contents, e)
	if e.HaveErrors() {
		return nil
	}

	var s ScenarioGroup
	if err := UnmarshalJSON(contents, &s); err != nil {
		e.Error(err)
		return nil
	}
	if s.Name == "" {
		e.ErrorString("scenario group is missing \"name\"")
		return nil
	}
	return &s
}

type RootFS struct{}

func (r RootFS) Open(filename string) (fs.File, error) {
	return os.Open(filename)
}

// LoadScenarioGroups loads all of the available scenarios, both from the
// scenarios/ directory in the source code distribution as well as,
// optionally, a scenario file provided on the command line.  It doesn't
// try to do any sort of meaningful error handling but it does try to
// continue on in the presence of errors; all errors will be printed and
// the program will exit if there are any.  We'd rather force any errors
// due to invalid scenario definitions to be fixed...
func LoadScenarioGroups(e *ErrorLogger) (map[string]*ScenarioGroup, map[string]*SimConfiguration) {
	// First load the embedded video maps.
	videoMapCommandBuffers := make(map[string]map[string]CommandBuffer)
	err := fs.WalkDir(embeddedVideoMaps, "videomaps", func(path string, d fs.DirEntry, err error) error {
		if d.IsDir() {
			return nil
		}

		lg.Printf("%s: loading embedded video map", path)
		vm := loadVideoMaps(embeddedVideoMaps, path, e)
		if vm != nil {
			videoMapCommandBuffers[path] = vm
		}
		return nil
	})
	if err != nil {
		lg.Errorf("%v", err)
		os.Exit(1)
	}

	// Load the video map specified on the command line, if any.
	loadVid := func(filename string) {
		if filename != "" {
			fs := func() fs.FS {
				if filepath.IsAbs(filename) {
					return RootFS{}
				} else {
					return os.DirFS(".")
				}
			}()
			vm := loadVideoMaps(fs, filename, e)
			if vm != nil {
				videoMapCommandBuffers[filename] = vm
			}
		}
	}
	loadVid(*videoMapFilename)
	if globalConfig != nil {
		loadVid(globalConfig.DevVideoMapFile)
	}

	// Now load the scenarios.
	scenarioGroups := make(map[string]*ScenarioGroup)
	simConfigurations := make(map[string]*SimConfiguration)

	err = fs.WalkDir(embeddedScenarioGroups, "scenarios", func(path string, d fs.DirEntry, err error) error {
		if d.IsDir() {
			return nil
		}

		lg.Printf("%s: loading embedded scenario", path)
		s := loadScenarioGroup(embeddedScenarioGroups, path, e)
		if s != nil {
			if _, ok := scenarioGroups[s.Name]; ok {
				e.ErrorString("%s: scenario redefined", s.Name)
			} else {
				scenarioGroups[s.Name] = s
			}
		}
		return nil
	})
	if err != nil {
		lg.Errorf("%v", err)
		os.Exit(1)
	}

	// Load the scenario specified on command line, if any.
	loadScenario := func(filename string) {
		if filename != "" {
			fs := func() fs.FS {
				if filepath.IsAbs(filename) {
					return RootFS{}
				} else {
					return os.DirFS(".")
				}
			}()
			s := loadScenarioGroup(fs, filename, e)
			if s != nil {
				// These may have an empty "video_map_file" member, which
				// is automatically patched up here...
				if s.VideoMapFile == "" {
					s.VideoMapFile = globalConfig.DevVideoMapFile
					if s.VideoMapFile == "" {
						s.VideoMapFile = *videoMapFilename
					}
				}

				// These are allowed to redefine an existing scenario.
				scenarioGroups[s.Name] = s
			}
		}
	}
	loadScenario(*scenarioFilename)
	if globalConfig != nil {
		loadScenario(globalConfig.DevScenarioFile)
	}

	// Final tidying before we return the loaded scenarios.
	for name, sgroup := range scenarioGroups {
		e.Push("Scenario group " + name)

		// Initialize the CommandBuffers in the scenario's STARSMaps.
		if sgroup.VideoMapFile == "" {
			e.ErrorString("no \"video_map_file\" specified")
		} else {
			if bufferMap, ok := videoMapCommandBuffers[sgroup.VideoMapFile]; !ok {
				e.ErrorString("video map file \"%s\" unknown", sgroup.VideoMapFile)
			} else {
				for i, sm := range sgroup.STARSMaps {
					if cb, ok := bufferMap[sm.Name]; !ok {
						e.ErrorString("video map \"%s\" not found", sm.Name)
					} else {
						sgroup.STARSMaps[i].CommandBuffer = cb
					}
				}
			}
		}

		sgroup.PostDeserialize(e, simConfigurations)

		e.Pop()
	}

	return scenarioGroups, simConfigurations
}

///////////////////////////////////////////////////////////////////////////
// Multi-controller utilities

func GetPrimaryController(multi map[string]*MultiUserController) (string, bool) {
	for callsign, mc := range multi {
		if mc.Primary {
			return callsign, true
		}
	}
	return "", false
}

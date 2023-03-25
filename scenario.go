// scenario.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

type ScenarioGroup struct {
	Name                 string                 `json:"name"`
	Airports             map[string]*Airport    `json:"airports"`
	VideoMapFile         string                 `json:"video_map_file"`
	Fixes                map[string]Point2LL    `json:"fixes"`
	Scenarios            map[string]*Scenario   `json:"scenarios"`
	DefaultController    string                 `json:"default_controller"`
	DefaultScenarioGroup string                 `json:"default_scenario"`
	ControlPositions     map[string]*Controller `json:"control_positions"`
	Scratchpads          map[string]string      `json:"scratchpads"`
	Airspace             Airspace               `json:"airspace"`
	ArrivalGroups        map[string][]Arrival   `json:"arrival_groups"`

	Center         Point2LL              `json:"center"`
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
	Route           string                   `json:"route"`

	InitialController string `json:"initial_controller"`
	InitialAltitude   int    `json:"initial_altitude"`
	ClearedAltitude   int    `json:"cleared_altitude"`
	InitialSpeed      int    `json:"initial_speed"`
	SpeedRestriction  int    `json:"speed_restriction"`
	ExpectApproach    string `json:"expect_approach"`
	Scratchpad        string `json:"scratchpad"`

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
	Callsign    string   `json:"callsign"`
	Wind        Wind     `json:"wind"`
	Controllers []string `json:"controllers"`

	// Map from arrival group name to map from airport name to rate...
	ArrivalGroupRates map[string]map[string]*int32 `json:"arrivals"`

	// Key is arrival group name
	nextArrivalSpawn map[string]time.Time

	ApproachAirspace       []AirspaceVolume `json:"-"`
	DepartureAirspace      []AirspaceVolume `json:"-"`
	ApproachAirspaceNames  []string         `json:"approach_airspace"`
	DepartureAirspaceNames []string         `json:"departure_airspace"`

	DepartureRunways []ScenarioGroupDepartureRunway `json:"departure_runways,omitempty"`
	ArrivalRunways   []ScenarioGroupArrivalRunway   `json:"arrival_runways,omitempty"`

	DefaultMap string `json:"default_map"`

	// The same runway may be present multiple times in DepartureRunways,
	// with different Category values. However, we want to make sure that
	// we don't spawn two aircraft on the same runway at the same time (or
	// close to it).  Therefore, here we track a per-runway "when's the
	// next time that we will spawn *something* from the runway" time.
	// When the time is up, we'll figure out which specific matching entry
	// in DepartureRunways to use...
	nextDepartureSpawn map[string]time.Time
}

type ScenarioGroupDepartureRunway struct {
	Airport  string `json:"airport"`
	Runway   string `json:"runway"`
	Category string `json:"category,omitempty"`
	Rate     int32  `json:"rate"`

	lastDeparture *Departure
	exitRoutes    map[string]ExitRoute // copied from DepartureRunway
}

type ScenarioGroupArrivalRunway struct {
	Airport string `json:"airport"`
	Runway  string `json:"runway"`
}

type Wind struct {
	Direction int32 `json:"direction"`
	Speed     int32 `json:"speed"`
	Gust      int32 `json:"gust"`
}

func (s *Scenario) AllAirports() []string {
	return append(s.DepartureAirports(), s.ArrivalAirports()...)
}

func (s *Scenario) DepartureAirports() []string {
	m := make(map[string]interface{})
	for _, rwy := range s.DepartureRunways {
		m[rwy.Airport] = nil
	}
	return SortedMapKeys(m)
}

func (s *Scenario) ArrivalAirports() []string {
	m := make(map[string]interface{})
	for _, rwy := range s.ArrivalRunways {
		m[rwy.Airport] = nil
	}
	return SortedMapKeys(m)
}

func (s *Scenario) runwayDepartureRate(ar string) int {
	r := 0
	for _, rwy := range s.DepartureRunways {
		if ar == rwy.Airport+"/"+rwy.Runway {
			r += int(rwy.Rate)
		}
	}
	return r
}

func (s *Scenario) Name() string {
	for _, sgroup := range scenarioGroups {
		for name, scenario := range sgroup.Scenarios {
			if s == scenario {
				return name
			}
		}
	}
	return "(unknown)"
}

func (s *Scenario) PostDeserialize(t *ScenarioGroup) []error {
	var errors []error

	for _, as := range s.ApproachAirspaceNames {
		if vol, ok := t.Airspace.Volumes[as]; !ok {
			errors = append(errors, fmt.Errorf("%s: unknown approach airspace", as))
		} else {
			s.ApproachAirspace = append(s.ApproachAirspace, vol...)
		}
	}
	for _, as := range s.DepartureAirspaceNames {
		if vol, ok := t.Airspace.Volumes[as]; !ok {
			errors = append(errors, fmt.Errorf("%s: unknown departure airspace", as))
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

	s.nextDepartureSpawn = make(map[string]time.Time)
	for i, rwy := range s.DepartureRunways {
		if ap, ok := t.Airports[rwy.Airport]; !ok {
			errors = append(errors, fmt.Errorf("%s: airport not found for departure runway", rwy.Airport))
		} else {
			if routes, ok := ap.DepartureRoutes[rwy.Runway]; !ok {
				errors = append(errors, fmt.Errorf("%s: runway not found at airport %s for departure runway",
					rwy.Runway, rwy.Airport))
			} else {
				s.DepartureRunways[i].exitRoutes = routes
			}
			s.nextDepartureSpawn[rwy.Airport+"/"+rwy.Runway] = time.Time{}

			if rwy.Category != "" {
				found := false
				for _, dep := range ap.Departures {
					if ap.ExitCategories[dep.Exit] == rwy.Category {
						found = true
						break
					}
				}
				if !found {
					errors = append(errors,
						fmt.Errorf("%s: no departures from %s have exit category specified for departure runway %s",
							rwy.Category, rwy.Airport, rwy.Runway))
				}
			}
		}
	}

	sort.Slice(s.ArrivalRunways, func(i, j int) bool {
		if s.ArrivalRunways[i].Airport == s.ArrivalRunways[j].Airport {
			return s.ArrivalRunways[i].Runway < s.ArrivalRunways[j].Runway
		}
		return s.ArrivalRunways[i].Airport < s.ArrivalRunways[j].Airport
	})

	s.nextArrivalSpawn = make(map[string]time.Time)

	for _, name := range SortedMapKeys(s.ArrivalGroupRates) {
		// Make sure the arrival group has been defined
		if arrivals, ok := t.ArrivalGroups[name]; !ok {
			errors = append(errors, fmt.Errorf("%s: arrival group not found in TRACON", name))
		} else {
			// Check the airports in it
			for airport := range s.ArrivalGroupRates[name] {
				if _, ok := t.Airports[airport]; !ok {
					errors = append(errors, fmt.Errorf("%s: unknown arrival airport in %s arrival group",
						airport, name))
				} else {
					found := false
					for _, ar := range arrivals {
						if _, ok := ar.Airlines[airport]; ok {
							found = true
							break
						}
					}
					if !found {
						errors = append(errors, fmt.Errorf("%s: airport not included in any arrivals in %s arrival group",
							airport, name))
					}
				}
			}
		}
	}

	for _, ctrl := range s.Controllers {
		if _, ok := t.ControlPositions[ctrl]; !ok {
			errors = append(errors, fmt.Errorf("%s: controller unknown", ctrl))
		}
	}

	if s.DefaultMap == "" {
		errors = append(errors, fmt.Errorf("must specify a default video map using \"default_map\""))
	} else {
		idx := FindIf(t.STARSMaps, func(m STARSMap) bool { return m.Name == s.DefaultMap })
		if idx == -1 {
			errors = append(errors, fmt.Errorf("%s: video map not found in scenario group's \"stars_maps\"", s.DefaultMap))
		}
	}

	return errors
}

///////////////////////////////////////////////////////////////////////////
// ScenarioGroup

func (t *ScenarioGroup) Locate(s string) (Point2LL, bool) {
	// ScenarioGroup's definitions take precedence...
	if ap, ok := t.Airports[s]; ok {
		return ap.Location, true
	} else if p, ok := t.Fixes[s]; ok {
		return p, true
	} else if n, ok := database.Navaids[strings.ToUpper(s)]; ok {
		return n.Location, ok
	} else if f, ok := database.Fixes[strings.ToUpper(s)]; ok {
		return f.Location, ok
	} else if p, err := ParseLatLong(s); err == nil {
		return p, true
	} else {
		return Point2LL{}, false
	}
}

func (t *ScenarioGroup) PostDeserialize(scenarioName string) {
	var errors []error

	for name, volumes := range t.Airspace.Volumes {
		for i, vol := range volumes {
			for _, b := range vol.BoundaryNames {
				if pts, ok := t.Airspace.Boundaries[b]; !ok {
					errors = append(errors, fmt.Errorf("%s: airspace boundary not found for airspace volume %s", b, name))
				} else {
					t.Airspace.Volumes[name][i].Boundaries = append(t.Airspace.Volumes[name][i].Boundaries, pts)
				}
			}
		}
	}

	for name, ap := range t.Airports {
		for _, err := range ap.PostDeserialize(t) {
			errors = append(errors, fmt.Errorf("%s: %v", name, err))
		}
	}

	if _, ok := t.Scenarios[t.DefaultScenarioGroup]; !ok {
		errors = append(errors, fmt.Errorf("%s: default scenario not found", t.DefaultScenarioGroup))
	}

	if _, ok := t.ControlPositions[t.DefaultController]; !ok {
		errors = append(errors, fmt.Errorf("%s: default controller not found", t.DefaultController))
	} else {
		// make sure the controller has at least one scenario..
		found := false
		for _, sc := range t.Scenarios {
			if sc.Callsign == t.DefaultController {
				found = true
				break
			}
		}
		if !found {
			errors = append(errors, fmt.Errorf("%s: default controller not used in any scenarios",
				t.DefaultController))
		}
	}

	if len(t.RadarSites) == 0 {
		errors = append(errors, fmt.Errorf("No radar sites specified"))
	}
	for name, rs := range t.RadarSites {
		if _, ok := t.Locate(rs.Position); rs.Position == "" || !ok {
			errors = append(errors, fmt.Errorf("%s: radar site position not found", name))
		} else if rs.Char == "" {
			errors = append(errors, fmt.Errorf("%s: radar site missing character id", name))
		}
	}

	for name, arrivals := range t.ArrivalGroups {
		if len(arrivals) == 0 {
			errors = append(errors, fmt.Errorf("%s: no arrivals in arrival group", name))
		}

		for _, ar := range arrivals {
			for _, err := range t.InitializeWaypointLocations(ar.Waypoints) {
				errors = append(errors, fmt.Errorf("%s: %v", name, err))
			}
			for _, wp := range ar.RunwayWaypoints {
				for _, err := range t.InitializeWaypointLocations(wp) {
					errors = append(errors, fmt.Errorf("%s: %v", name, err))
				}
			}

			for _, apAirlines := range ar.Airlines {
				for _, al := range apAirlines {
					for _, err := range database.CheckAirline(al.ICAO, al.Fleet) {
						errors = append(errors, err)
					}
				}
			}

			if _, ok := t.ControlPositions[ar.InitialController]; !ok {
				errors = append(errors, fmt.Errorf("%s: controller not found for arrival in %s group",
					ar.InitialController, name))
			}
		}
	}

	// Do after airports!
	for name, s := range t.Scenarios {
		for _, err := range s.PostDeserialize(t) {
			errors = append(errors, fmt.Errorf("scenario %s: %v", name, err))
		}
	}

	if len(errors) > 0 {
		for _, err := range errors {
			lg.Errorf("%s: %v", scenarioName, err)
		}
		os.Exit(1)
	}
}

func (t *ScenarioGroup) InitializeWaypointLocations(waypoints []Waypoint) []error {
	var prev Point2LL
	var errors []error

	for i, wp := range waypoints {
		if pos, ok := t.Locate(wp.Fix); ok {
			waypoints[i].Location = pos
		} else {
			errors = append(errors, fmt.Errorf("%s: unable to locate waypoint", wp.Fix))
			continue
		}

		d := nmdistance2ll(prev, waypoints[i].Location)
		if i > 1 && d > 50 {
			errors = append(errors, fmt.Errorf("%s: waypoint at %s is suspiciously far from previous one (%s at %s): %f nm",
				wp.Fix, waypoints[i].Location.DDString(), waypoints[i-1].Fix, waypoints[i-1].Location.DDString(), d))
		}
		prev = waypoints[i].Location
	}
	return errors
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

func loadVideoMaps(filesystem fs.FS, path string) (map[string]CommandBuffer, error) {
	contents, err := fs.ReadFile(filesystem, path)
	if err != nil {
		return nil, err
	}

	if strings.HasSuffix(strings.ToLower(path), ".zst") {
		contents = []byte(decompressZstd(string(contents)))
	}

	var maps map[string][]Point2LL
	if err := UnmarshalJSON(contents, &maps); err != nil {
		return nil, err
	}

	vm := make(map[string]CommandBuffer)
	for name, segs := range maps {
		if _, ok := vm[name]; ok {
			return nil, fmt.Errorf("%s: video map repeatedly defined in file %s", name, path)
		}

		ld := GetLinesDrawBuilder()
		for i := 0; i < len(segs)/2; i++ {
			ld.AddLine(segs[2*i], segs[2*i+1])
		}
		var cb CommandBuffer
		ld.GenerateCommands(&cb)

		vm[name] = cb

		ReturnLinesDrawBuilder(ld)
	}

	return vm, nil
}

func loadScenarioGroup(filesystem fs.FS, path string) (*ScenarioGroup, error) {
	contents, err := fs.ReadFile(filesystem, path)
	if err != nil {
		return nil, err
	}

	var s ScenarioGroup
	if err := UnmarshalJSON(contents, &s); err != nil {
		return nil, err
	}
	if s.Name == "" {
		return nil, fmt.Errorf("%s: scenario definition is missing a \"name\" member", path)
	}
	return &s, err
}

type RootFS struct{}

func (r RootFS) Open(filename string) (fs.File, error) {
	return os.Open(filename)
}

// LoadScenarioGroups loads all of the available scenarios, both from the
// scenarios/ directory in the source code distribution as well as,
// optionally, a scenario file provided on the command line.  It doesn't
// try to do any sort of meaningful error handling; we'd rather force any
// errors due to invalid scenario definitions to be fixed rather than
// trying to recover, so error messages are printed and the program exits
// if there are any issues..
func LoadScenarioGroups() map[string]*ScenarioGroup {
	// First load the embedded video maps.
	videoMapCommandBuffers := make(map[string]map[string]CommandBuffer)
	err := fs.WalkDir(embeddedVideoMaps, "videomaps", func(path string, d fs.DirEntry, err error) error {
		if d.IsDir() {
			return nil
		}

		lg.Printf("%s: loading embedded video map", path)
		vm, err := loadVideoMaps(embeddedVideoMaps, path)
		if err == nil {
			videoMapCommandBuffers[path] = vm
		}
		return err
	})
	if err != nil {
		lg.Errorf("%v", err)
		os.Exit(1)
	}

	// Load the video map specified on the command line, if any.
	for _, filename := range []string{*videoMapFilename, globalConfig.DevVideoMapFile} {
		if filename != "" {
			fs := func() fs.FS {
				if path.IsAbs(filename) {
					return RootFS{}
				} else {
					return os.DirFS(".")
				}
			}()
			vm, err := loadVideoMaps(fs, filename)
			if err != nil {
				lg.Errorf("%v", err)
				os.Exit(1)
			}
			videoMapCommandBuffers[filename] = vm
		}
	}

	// Now load the scenarios.
	scenarioGroups := make(map[string]*ScenarioGroup)
	err = fs.WalkDir(embeddedScenarioGroups, "scenarios", func(path string, d fs.DirEntry, err error) error {
		if d.IsDir() {
			return nil
		}

		lg.Printf("%s: loading embedded scenario", path)
		s, err := loadScenarioGroup(embeddedScenarioGroups, path)
		if err != nil {
			return err
		}
		if _, ok := scenarioGroups[s.Name]; ok {
			return fmt.Errorf("%s: scenario repeatedly defined", s.Name)
		}
		scenarioGroups[s.Name] = s
		return nil
	})
	if err != nil {
		lg.Errorf("%v", err)
		os.Exit(1)
	}

	// Load the scenario specified on command line, if any.
	for _, filename := range []string{*scenarioFilename, globalConfig.DevScenarioFile} {
		if filename != "" {
			fs := func() fs.FS {
				if path.IsAbs(filename) {
					return RootFS{}
				} else {
					return os.DirFS(".")
				}
			}()
			s, err := loadScenarioGroup(fs, filename)
			if err != nil {
				lg.Errorf("%v", err)
				os.Exit(1)
			}

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

	// Final tidying before we return the loaded scenarios.
	for groupName, sgroup := range scenarioGroups {
		// Initialize the CommandBuffers in the scenario's STARSMaps.
		if sgroup.VideoMapFile == "" {
			lg.Errorf("%s: scenario does not have \"video_map_file\" specified", sgroup.Name)
			os.Exit(1)
		}
		if bufferMap, ok := videoMapCommandBuffers[sgroup.VideoMapFile]; !ok {
			lg.Errorf("%s: video map file not found for scenario %s", sgroup.VideoMapFile, sgroup.Name)
			os.Exit(1)
		} else {
			for i, sm := range sgroup.STARSMaps {
				if cb, ok := bufferMap[sm.Name]; !ok {
					lg.Errorf("%s: video map not found for scenario %s", sm.Name, sgroup.Name)
					os.Exit(1)
				} else {
					sgroup.STARSMaps[i].cb = cb
				}
			}
		}

		// This is horribly hacky but PostDeserialize ends up calling
		// functions that access the scenario global
		// (e.g. nmdistance2ll)...
		scenarioGroup = sgroup
		sgroup.PostDeserialize(groupName)
		scenarioGroup = nil
	}

	return scenarioGroups
}

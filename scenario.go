// scenario.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"
)

type ScenarioGroup struct {
	Name                 string                      `json:"name"`
	Airports             map[string]*Airport         `json:"airports"`
	VideoMapFile         string                      `json:"video_map_file"`
	Fixes                map[string]Point2LL         `json:"fixes"`
	Scenarios            map[string]*Scenario        `json:"scenarios"`
	DefaultController    string                      `json:"default_controller"`
	DefaultScenarioGroup string                      `json:"default_scenario"`
	ControlPositions     map[string]*Controller      `json:"control_positions"`
	Scratchpads          map[string]string           `json:"scratchpads"`
	AirspaceBoundaries   map[string][]Point2LL       `json:"airspace_boundaries"`
	AirspaceVolumes      map[string][]AirspaceVolume `json:"airspace_volumes"`
	ArrivalGroups        map[string][]Arrival        `json:"arrival_groups"`

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

type AirspaceVolume struct {
	LowerLimit    int          `json:"lower"`
	UpperLimit    int          `json:"upper"`
	Boundaries    [][]Point2LL `json:"-"`
	BoundaryNames []string     `json:"boundaries"`
}

type Scenario struct {
	Name        string   `json:"name"`
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

func (s *Scenario) PostDeserialize(t *ScenarioGroup) []error {
	var errors []error

	for _, as := range s.ApproachAirspaceNames {
		if vol, ok := t.AirspaceVolumes[as]; !ok {
			errors = append(errors, fmt.Errorf("%s: unknown approach airspace in scenario %s", as, s.Name))
		} else {
			s.ApproachAirspace = append(s.ApproachAirspace, vol...)
		}
	}
	for _, as := range s.DepartureAirspaceNames {
		if vol, ok := t.AirspaceVolumes[as]; !ok {
			errors = append(errors, fmt.Errorf("%s: unknown departure airspace in scenario %s", as, s.Name))
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
			errors = append(errors, fmt.Errorf("%s: airport not found for departure runway in scenario %s", rwy.Airport, s.Name))
		} else {
			idx := FindIf(ap.DepartureRunways, func(r *DepartureRunway) bool { return r.Runway == rwy.Runway })
			if idx == -1 {
				errors = append(errors, fmt.Errorf("%s: runway not found at airport %s for departure runway in scenario %s",
					rwy.Runway, rwy.Airport, s.Name))
			} else {
				s.DepartureRunways[i].exitRoutes = ap.DepartureRunways[idx].ExitRoutes
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
						fmt.Errorf("%s: no departures from %s have exit category specified for departure runway %s in scenario %s",
							rwy.Category, rwy.Airport, rwy.Runway, s.Name))
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

	for _, rwy := range s.ArrivalRunways {
		if ap, ok := t.Airports[rwy.Airport]; !ok {
			errors = append(errors, fmt.Errorf("%s: airport not found for arrival runway in scenario %s", rwy.Airport, s.Name))
		} else if FindIf(ap.ArrivalRunways, func(r *ArrivalRunway) bool { return r.Runway == rwy.Runway }) == -1 {
			errors = append(errors, fmt.Errorf("%s: runway not found for arrival runway at airport %s in scenario %s",
				rwy.Runway, rwy.Airport, s.Name))
		}
	}

	s.nextArrivalSpawn = make(map[string]time.Time)

	for _, name := range SortedMapKeys(s.ArrivalGroupRates) {
		// Make sure the arrival group has been defined
		if arrivals, ok := t.ArrivalGroups[name]; !ok {
			errors = append(errors, fmt.Errorf("%s: arrival group not found in TRACON in scenario %s", name, s.Name))
		} else {
			// Check the airports in it
			for airport := range s.ArrivalGroupRates[name] {
				if _, ok := t.Airports[airport]; !ok {
					errors = append(errors, fmt.Errorf("%s: unknown arrival airport in %s arrival group in scenario %s",
						airport, name, s.Name))
				} else {
					found := false
					for _, ar := range arrivals {
						if _, ok := ar.Airlines[airport]; ok {
							found = true
							break
						}
					}
					if !found {
						errors = append(errors, fmt.Errorf("%s: airport not included in any arrivals in %s arrival group in scenario %s",
							airport, name, s.Name))
					}
				}
			}
		}
	}

	for _, ctrl := range s.Controllers {
		if _, ok := t.ControlPositions[ctrl]; !ok {
			errors = append(errors, fmt.Errorf("%s: controller unknown in scenario %s", ctrl, s.Name))
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

func (t *ScenarioGroup) PostDeserialize() {
	var errors []error

	for name, volumes := range t.AirspaceVolumes {
		for i, vol := range volumes {
			for _, b := range vol.BoundaryNames {
				if pts, ok := t.AirspaceBoundaries[b]; !ok {
					errors = append(errors, fmt.Errorf("%s: airspace boundary not found for airspace volume %s", b, name))
				} else {
					t.AirspaceVolumes[name][i].Boundaries = append(t.AirspaceVolumes[name][i].Boundaries, pts)
				}
			}
		}
	}

	for name, ap := range t.Airports {
		if name != ap.ICAO {
			errors = append(errors, fmt.Errorf("%s: airport Name doesn't match (%s)", name, ap.ICAO))
		}
		for _, err := range ap.PostDeserialize(t) {
			errors = append(errors, fmt.Errorf("%s: error in specification: %v", ap.ICAO, err))
		}
	}

	if _, ok := t.Scenarios[t.DefaultScenarioGroup]; !ok {
		errors = append(errors, fmt.Errorf("%s: default scenario not found in %s", t.DefaultScenarioGroup, t.Name))
	}

	if _, ok := t.ControlPositions[t.DefaultController]; !ok {
		errors = append(errors, fmt.Errorf("%s: default controller not found in %s", t.DefaultController, t.Name))
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
			errors = append(errors, fmt.Errorf("%s: default controller not used in any scenarios in %s",
				t.DefaultController, t.Name))
		}
	}

	if len(t.RadarSites) == 0 {
		errors = append(errors, fmt.Errorf("No radar sites specified in tracon %s", t.Name))
	}
	for name, rs := range t.RadarSites {
		if _, ok := t.Locate(rs.Position); rs.Position == "" || !ok {
			errors = append(errors, fmt.Errorf("%s: radar site position not found in %s", name, t.Name))
		} else if rs.Char == "" {
			errors = append(errors, fmt.Errorf("%s: radar site missing character id in %s", name, t.Name))
		}
	}

	for name, arrivals := range t.ArrivalGroups {
		if len(arrivals) == 0 {
			errors = append(errors, fmt.Errorf("%s: no arrivals in arrival group in %s", name, t.Name))
		}

		for _, ar := range arrivals {
			for _, err := range t.InitializeWaypointLocations(ar.Waypoints) {
				errors = append(errors, fmt.Errorf("%s: %v in %s", name, err, t.Name))
			}
			for _, wp := range ar.RunwayWaypoints {
				for _, err := range t.InitializeWaypointLocations(wp) {
					errors = append(errors, fmt.Errorf("%s: %v in %s", name, err, t.Name))
				}
			}

			for _, apAirlines := range ar.Airlines {
				for _, al := range apAirlines {
					for _, err := range database.CheckAirline(al.ICAO, al.Fleet) {
						errors = append(errors, fmt.Errorf("%v in %s", err, t.Name))
					}
				}
			}

			if _, ok := t.ControlPositions[ar.InitialController]; !ok {
				errors = append(errors, fmt.Errorf("%s: controller not found for arrival in %s group in %s",
					ar.InitialController, name, t.Name))
			}
		}
	}

	// Do after airports!
	for _, s := range t.Scenarios {
		errors = append(errors, s.PostDeserialize(t)...)
	}

	if len(errors) > 0 {
		for _, err := range errors {
			lg.Errorf("%v", err)
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
	if *videoMapFilename != "" {
		vm, err := loadVideoMaps(os.DirFS("."), *videoMapFilename)
		if err != nil {
			lg.Errorf("%v", err)
			os.Exit(1)
		}
		videoMapCommandBuffers[*videoMapFilename] = vm
	}

	// Now load the scenarios.
	scenarios := make(map[string]*ScenarioGroup)
	err = fs.WalkDir(embeddedScenarioGroups, "scenarios", func(path string, d fs.DirEntry, err error) error {
		if d.IsDir() {
			return nil
		}

		lg.Printf("%s: loading embedded scenario", path)
		s, err := loadScenarioGroup(embeddedScenarioGroups, path)
		if err != nil {
			return err
		}
		if _, ok := scenarios[s.Name]; ok {
			return fmt.Errorf("%s: scenario repeatedly defined", s.Name)
		}
		scenarios[s.Name] = s
		return nil
	})
	if err != nil {
		lg.Errorf("%v", err)
		os.Exit(1)
	}

	// Load the scenario specified on command line, if any.
	if *scenarioFilename != "" {
		s, err := loadScenarioGroup(os.DirFS("."), *scenarioFilename)
		if err != nil {
			lg.Errorf("%v", err)
			os.Exit(1)
		}
		// This one is allowed to redefine an existing scenario.
		scenarios[s.Name] = s
	}

	// Final tidying before we return the loaded scenarios.
	for _, s := range scenarios {
		// Initialize the CommandBuffers in the scenario's STARSMaps.
		if s.VideoMapFile == "" {
			lg.Errorf("%s: scenario does not have \"video_map_file\" specified", s.Name)
			os.Exit(1)
		}
		if bufferMap, ok := videoMapCommandBuffers[s.VideoMapFile]; !ok {
			lg.Errorf("%s: \"video_map_file\" not found for scenario %s", s.VideoMapFile, s.Name)
			os.Exit(1)
		} else {
			for i, sm := range s.STARSMaps {
				if cb, ok := bufferMap[sm.Name]; !ok {
					lg.Errorf("%s: video map not found for scenario %s", sm.Name, s.Name)
					os.Exit(1)
				} else {
					s.STARSMaps[i].cb = cb
				}
			}
		}

		// This is horribly hacky but PostDeserialize ends up calling
		// functions that access the scenario global
		// (e.g. nmdistance2ll)...
		scenario = s
		s.PostDeserialize()
		scenario = nil
	}

	return scenarios
}

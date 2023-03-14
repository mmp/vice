// scenario.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	_ "embed"
	"encoding/xml"
	"fmt"
	"os"
	"sort"
	"strings"
)

type Scenario struct {
	Name              string                      `json:"name"`
	Airports          map[string]*Airport         `json:"airports"`
	VideoMapFile      string                      `json:"video_map_file"`
	Fixes             map[string]Point2LL         `json:"fixes"`
	ScenarioConfigs   map[string]*ScenarioConfig  `json:"configs"`
	DefaultController string                      `json:"default_controller"`
	DefaultScenario   string                      `json:"default_scenario"`
	ControlPositions  map[string]*Controller      `json:"control_positions"`
	Scratchpads       map[string]string           `json:"scratchpads"`
	AirspaceVolumes   map[string][]AirspaceVolume `json:"-"` // for now, parsed from the XML...
	ArrivalGroups     map[string][]Arrival        `json:"arrival_groups"`

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
	LowerLimit, UpperLimit int
	Boundaries             [][]Point2LL
}

func (t *Scenario) Locate(s string) (Point2LL, bool) {
	// Scenario's definitions take precedence...
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

func (t *Scenario) PostDeserialize() {
	t.AirspaceVolumes = parseAirspace()

	var errors []error
	for name, ap := range t.Airports {
		if name != ap.ICAO {
			errors = append(errors, fmt.Errorf("%s: airport Name doesn't match (%s)", name, ap.ICAO))
		}
		for _, err := range ap.PostDeserialize(t) {
			errors = append(errors, fmt.Errorf("%s: error in specification: %v", ap.ICAO, err))
		}
	}

	if _, ok := t.ScenarioConfigs[t.DefaultScenario]; !ok {
		errors = append(errors, fmt.Errorf("%s: default scenario not found in %s", t.DefaultScenario, t.Name))
	}

	if _, ok := t.ControlPositions[t.DefaultController]; !ok {
		errors = append(errors, fmt.Errorf("%s: default controller not found in %s", t.DefaultController, t.Name))
	} else {
		// make sure the controller has at least one scenario..
		found := false
		for _, sc := range t.ScenarioConfigs {
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
	for _, s := range t.ScenarioConfigs {
		errors = append(errors, s.PostDeserialize(t)...)
	}

	if len(errors) > 0 {
		for _, err := range errors {
			lg.Errorf("%v", err)
		}
		os.Exit(1)
	}
}

func (t *Scenario) InitializeWaypointLocations(waypoints []Waypoint) []error {
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

//go:embed resources/ZNY_sanscomment_VOLUMES.xml
var znyVolumesXML string

type XMLBoundary struct {
	Name     string `xml:"Name,attr"`
	Segments string `xml:",chardata"`
}

type XMLVolume struct {
	Name       string `xml:"Name,attr"`
	LowerLimit int    `xml:"LowerLimit,attr"`
	UpperLimit int    `xml:"UpperLimit,attr"`
	Boundaries string `xml:"Boundaries"`
}

type XMLAirspace struct {
	XMLName    xml.Name      `xml:"Volumes"`
	Boundaries []XMLBoundary `xml:"Boundary"`
	Volumes    []XMLVolume   `xml:"Volume"`
}

func parseAirspace() map[string][]AirspaceVolume {
	var xair XMLAirspace
	if err := xml.Unmarshal([]byte(znyVolumesXML), &xair); err != nil {
		panic(err)
	}

	//lg.Errorf("%s", spew.Sdump(vol))

	boundaries := make(map[string][]Point2LL)
	volumes := make(map[string][]AirspaceVolume)

	for _, b := range xair.Boundaries {
		var pts []Point2LL
		for _, ll := range strings.Split(b.Segments, "/") {
			p, err := ParseLatLong(strings.TrimSpace(ll))
			if err != nil {
				lg.Errorf("%s: %v", ll, err)
			} else {
				pts = append(pts, p)
			}
		}
		if _, ok := boundaries[b.Name]; ok {
			lg.Errorf("%s: boundary redefined", b.Name)
		}
		boundaries[b.Name] = pts
	}

	for _, v := range xair.Volumes {
		vol := AirspaceVolume{
			LowerLimit: v.LowerLimit,
			UpperLimit: v.UpperLimit,
		}

		for _, name := range strings.Split(v.Boundaries, ",") {
			if b, ok := boundaries[name]; !ok {
				lg.Errorf("%s: boundary in volume %s has not been defined. Volume may be invalid",
					name, v.Name)
			} else {
				vol.Boundaries = append(vol.Boundaries, b)
			}
		}

		volumes[v.Name] = append(volumes[v.Name], vol)
	}

	return volumes
}

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

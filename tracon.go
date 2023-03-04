// tracon.go
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
	"time"
)

type TRACON struct {
	Name              string                      `json:"name"`
	Airports          map[string]*Airport         `json:"airports"`
	Fixes             map[string]Point2LL         `json:"fixes"`
	VideoMaps         map[string]*VideoMap        `json:"-"`
	Scenarios         map[string]*Scenario        `json:"scenarios"`
	DefaultController string                      `json:"default_controller"`
	DefaultScenario   string                      `json:"default_scenario"`
	ControlPositions  map[string]*Controller      `json:"control_positions"`
	Scratchpads       map[string]string           `json:"scratchpads"`
	AirspaceVolumes   map[string][]AirspaceVolume `json:"-"` // for now, parsed from the XML...
	ArrivalGroups     []ArrivalGroup              `json:"arrival_groups"`

	Center         Point2LL    `json:"center"`
	PrimaryAirport string      `json:"primary_airport"`
	RadarSites     []RadarSite `json:"radar_sites"`
	STARSMaps      []STARSMap  `json:"stars_maps"`

	NmPerLatitude     float32 `json:"nm_per_latitude"`
	NmPerLongitude    float32 `json:"nm_per_longitude"`
	MagneticVariation float32 `json:"magnetic_variation"`
}

type ArrivalGroup struct {
	Name     string    `json:"name"`
	Arrivals []Arrival `json:"arrivals"`

	rates     map[string]*int32 // map from airport to arrival rate
	nextSpawn time.Time
}

func (ag *ArrivalGroup) Airports() []string {
	m := make(map[string]interface{})
	for _, ar := range ag.Arrivals {
		for al := range ar.Airlines {
			m[al] = nil
		}
	}
	return SortedMapKeys(m)
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

func (t *TRACON) Locate(s string) (Point2LL, bool) {
	// TRACON's definitions take precedence...
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

func (t *TRACON) PostDeserialize() {
	t.AirspaceVolumes = parseAirspace()

	var errors []error
	for _, ap := range t.Airports {
		for _, err := range ap.PostDeserialize(t) {
			errors = append(errors, fmt.Errorf("%s: error in specification: %v", ap.ICAO, err))
		}
	}

	if _, ok := t.Scenarios[t.DefaultScenario]; !ok {
		errors = append(errors, fmt.Errorf("%s: default scenario not found.", t.DefaultScenario))
	}

	if _, ok := t.ControlPositions[t.DefaultController]; !ok {
		errors = append(errors, fmt.Errorf("%s: default controller not found.", t.DefaultController))
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

	for _, ag := range t.ArrivalGroups {
		if len(ag.Arrivals) == 0 {
			errors = append(errors, fmt.Errorf("%s: no arrivals in arrival group", ag.Name))
		}

		for _, ar := range ag.Arrivals {
			for _, err := range t.InitializeWaypointLocations(ar.Waypoints) {
				errors = append(errors, fmt.Errorf("%s: %v", ag.Name, err))
			}
			for _, wp := range ar.RunwayWaypoints {
				for _, err := range t.InitializeWaypointLocations(wp) {
					errors = append(errors, fmt.Errorf("%s: %v", ag.Name, err))
				}
			}

			for _, apAirlines := range ar.Airlines {
				for _, al := range apAirlines {
					for _, err := range database.CheckAirline(al.ICAO, al.Fleet) {
						errors = append(errors, fmt.Errorf("%v", err))
					}
				}
			}

			if _, ok := t.ControlPositions[ar.InitialController]; !ok {
				errors = append(errors, fmt.Errorf("%s: controller not found for arrival in %s group", ar.InitialController, ag.Name))
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

	if globalConfig.Version < 2 && globalConfig.DisplayRoot != nil {
		// Add the PHL airport and radar sites...
		// All of the following is quite brittle / hard-coded and
		// doesn't really have any error handling (but we control the
		// input, so it all "should" be fine...)
		stars := globalConfig.DisplayRoot.Children[0].Pane.(*STARSPane)
		stars.Facility.Airports = append(stars.Facility.Airports,
			STARSAirport{ICAOCode: "KPHL", Range: 60, IncludeInSSA: true})

		phl := FindIf(tracon.RadarSites, func(r RadarSite) bool { return r.Id == "PHL" })
		stars.Facility.RadarSites = append(stars.Facility.RadarSites, tracon.RadarSites[phl])
		nxx := FindIf(tracon.RadarSites, func(r RadarSite) bool { return r.Id == "NXX" })
		stars.Facility.RadarSites = append(stars.Facility.RadarSites, tracon.RadarSites[nxx])

		for i := 0; i < 2; i++ {
			stars.currentPreferenceSet.RadarSiteSelected = append(stars.currentPreferenceSet.RadarSiteSelected, false)
			for j := range stars.PreferenceSets {
				stars.PreferenceSets[j].RadarSiteSelected = append(stars.PreferenceSets[j].RadarSiteSelected, false)
			}
		}

		globalConfig.Version = 2
	}
}

func (t *TRACON) InitializeWaypointLocations(waypoints []Waypoint) []error {
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

// tracon.go

package main

import (
	_ "embed"
	"encoding/xml"
	"sort"
	"strings"
)

// TRACON and Scenario are all (mostly) read only
// though do we allow Rate to be changed and stay persistent? (probs...)

type TRACON struct {
	Name             string                      `json:"name"`
	Airports         map[string]*Airport         `json:"airports"`
	Fixes            map[string]Point2LL         `json:"fixes"`
	VideoMaps        map[string]*VideoMap        `json:"-"`
	Scenarios        map[string]*Scenario        `json:"scenarios"`
	DefaultScenario  string                      `json:"default_scenario"`
	ControlPositions map[string]*Controller      `json:"control_positions"`
	Scratchpads      map[string]string           `json:"scratchpads"`
	AirspaceVolumes  map[string][]AirspaceVolume `json:"-"` // for now, parsed from the XML...

	Center         Point2LL    `json:"center"`
	PrimaryAirport string      `json:"primary_airport"`
	RadarSites     []RadarSite `json:"radar_sites"`
	STARSMaps      []STARSMap  `json:"stars_maps"`

	NmPerLatitude     float32 `json:"nm_per_latitude"`
	NmPerLongitude    float32 `json:"nm_per_longitude"`
	MagneticVariation float32 `json:"magnetic_variation"`
}

type AirspaceVolume struct {
	LowerLimit, UpperLimit int
	Boundaries             [][]Point2LL
}

func (t *TRACON) Locate(s string) (Point2LL, bool) {
	if ap, ok := t.Airports[s]; ok {
		return ap.Location, true
	} else if p, ok := t.Fixes[s]; ok {
		return p, true
	} else {
		return Point2LL{}, false
	}
}

func (t *TRACON) PostDeserialize() {
	t.AirspaceVolumes = parseAirspace()

	for _, ap := range t.Airports {
		if errors := ap.PostDeserialize(t.ControlPositions); len(errors) > 0 {
			for _, err := range errors {
				lg.Errorf("%s: error in specification: %v", ap.ICAO, err)
			}
		}
	}

	if _, ok := t.Scenarios[t.DefaultScenario]; !ok {
		lg.Errorf("%s: default scenario not found", t.DefaultScenario)
	}

	// Do after airports!
	for _, s := range t.Scenarios {
		if errors := s.PostDeserialize(t); len(errors) > 0 {
			for _, err := range errors {
				lg.Errorf("%s: error in specification: %v", s.Name, err)
			}
		}
	}

	if globalConfig.Version < 2 {
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

func (t *TRACON) ActivateScenario(s string) {
	scenario, ok := t.Scenarios[s]
	if !ok {
		panic(s + " not found")
	}

	// Disable all runways
	for _, ap := range t.Airports {
		for _, rwy := range ap.DepartureRunways {
			rwy.Enabled = false
		}
		for _, rwy := range ap.ArrivalRunways {
			rwy.Enabled = false
		}
	}

	// Enable the ones from the scenario
	for _, dep := range scenario.DepartureRunways {
		dep.Enabled = true
	}
	for _, arr := range scenario.ArrivalRunways {
		arr.Enabled = true
	}
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

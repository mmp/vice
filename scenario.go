// scenario.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iancoleman/orderedmap"
	"golang.org/x/exp/slog"
)

type ScenarioGroup struct {
	Name             string                 `json:"name"`
	Airports         map[string]*Airport    `json:"airports"`
	VideoMapFile     string                 `json:"video_map_file"`
	Fixes            map[string]Point2LL    `json:"-"`
	FixesStrings     orderedmap.OrderedMap  `json:"fixes"`
	Scenarios        map[string]*Scenario   `json:"scenarios"`
	DefaultScenario  string                 `json:"default_scenario"`
	ControlPositions map[string]*Controller `json:"control_positions"`
	Scratchpads      map[string]string      `json:"scratchpads"`
	Airspace         Airspace               `json:"airspace"`
	ArrivalGroups    map[string][]Arrival   `json:"arrival_groups"`

	Center           Point2LL              `json:"-"`
	CenterString     string                `json:"center"`
	Range            float32               `json:"range"`
	PrimaryAirport   string                `json:"primary_airport"`
	RadarSites       map[string]*RadarSite `json:"radar_sites"`
	STARSMaps        []STARSMap            `json:"stars_maps"`
	InhibitCAVolumes []AirspaceVolume      `json:"inhibit_ca_volumes"`

	ReportingPointStrings []string         `json:"reporting_points"`
	ReportingPoints       []ReportingPoint // not in JSON

	NmPerLatitude     float32 `json:"nm_per_latitude"`
	NmPerLongitude    float32 `json:"nm_per_longitude"`
	MagneticVariation float32 `json:"magnetic_variation"`
}

type ReportingPoint struct {
	Fix      string
	Location Point2LL
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
	Description       string  `json:"description"`

	Airlines map[string][]ArrivalAirline `json:"airlines"`
}

type ArrivalAirline struct {
	ICAO    string `json:"icao"`
	Airport string `json:"airport"`
	Fleet   string `json:"fleet,omitempty"`
}

type Airspace struct {
	Boundaries map[string][]Point2LL                 `json:"boundaries"`
	Volumes    map[string][]ControllerAirspaceVolume `json:"volumes"`
}

type ControllerAirspaceVolume struct {
	LowerLimit    int          `json:"lower"`
	UpperLimit    int          `json:"upper"`
	Boundaries    [][]Point2LL `json:"boundary_polylines"` // not in JSON
	BoundaryNames []string     `json:"boundaries"`
}

type Scenario struct {
	SoloController      string                `json:"solo_controller"`
	SplitConfigurations SplitConfigurationSet `json:"multi_controllers"`
	DefaultSplit        string                `json:"default_split"`
	Wind                Wind                  `json:"wind"`
	VirtualControllers  []string              `json:"controllers"`

	// Map from arrival group name to map from airport name to default rate...
	ArrivalGroupDefaultRates map[string]map[string]int `json:"arrivals"`

	ApproachAirspace       []ControllerAirspaceVolume `json:"approach_airspace_volumes"`  // not in JSON
	DepartureAirspace      []ControllerAirspaceVolume `json:"departure_airspace_volumes"` // not in JSON
	ApproachAirspaceNames  []string                   `json:"approach_airspace"`
	DepartureAirspaceNames []string                   `json:"departure_airspace"`

	DepartureRunways []ScenarioGroupDepartureRunway `json:"departure_runways,omitempty"`
	ArrivalRunways   []ScenarioGroupArrivalRunway   `json:"arrival_runways,omitempty"`

	DefaultMap string `json:"default_map"`
}

// split -> config
type SplitConfigurationSet map[string]SplitConfiguration

// callsign -> controller contig
type SplitConfiguration map[string]*MultiUserController

type MultiUserController struct {
	Primary          bool     `json:"primary"`
	BackupController string   `json:"backup"`
	Departures       []string `json:"departures"`
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

	if _, ok := sg.ControlPositions[s.SoloController]; s.SoloController != "" && !ok {
		e.ErrorString("controller \"%s\" for \"solo_controller\" is unknown", s.SoloController)
	}

	// Various multi_controllers validations
	if len(s.SplitConfigurations) > 0 {
		if len(s.SplitConfigurations) == 1 && s.DefaultSplit == "" {
			// Set the default split to be the single specified controller
			// assignment.
			for s.DefaultSplit = range s.SplitConfigurations {
			}
		} else if s.DefaultSplit == "" {
			e.ErrorString("multiple splits specified in \"multi_controllers\" but no \"default_split\" specified")
		} else if _, ok := s.SplitConfigurations[s.DefaultSplit]; !ok {
			e.ErrorString("did not find \"default_split\" \"%s\" in \"multi_controllers\" splits", s.DefaultSplit)
		}
	}
	for name, controllers := range s.SplitConfigurations {
		primaryController := ""
		e.Push("\"multi_controllers\": split \"" + name + "\"")

		for callsign, ctrl := range controllers {
			e.Push(callsign)
			if ctrl.Primary {
				if primaryController != "" {
					e.ErrorString("multiple controllers specified as \"primary\": %s %s",
						primaryController, callsign)
				} else {
					primaryController = callsign
				}
			}

			// Make sure any airports claimed for departures are valid
			for _, airportRunway := range ctrl.Departures {
				ap, rwy, ok := strings.Cut(airportRunway, "/")
				if !ok { // no runway specified; take all runways
					pred := func(r ScenarioGroupDepartureRunway) bool {
						return r.Airport == ap
					}
					if FindIf(s.DepartureRunways, pred) == -1 {
						e.ErrorString("airport \"%s\" is not departing aircraft in this scenario", ap)
					}
				} else {
					pred := func(r ScenarioGroupDepartureRunway) bool {
						return r.Airport == ap && r.Runway == rwy
					}
					if FindIf(s.DepartureRunways, pred) == -1 {
						e.ErrorString("runway \"%s\" at airport \"%s\" is not departing aircraft in this scenario", rwy, ap)
					}
				}
			}

			// Make sure all arrivals are valid. Below we make sure all
			// included arrivals have a controller.
			for _, arr := range ctrl.Arrivals {
				if _, ok := s.ArrivalGroupDefaultRates[arr]; !ok {
					e.ErrorString("arrival \"%s\" not found in scenario", arr)
				}
			}
			e.Pop()
		}
		if primaryController == "" {
			e.ErrorString("No controller in \"multi_controllers\" was specified as \"primary\"")
		}

		// Make sure each active departing airport runway has exactly one
		// controller handling its departures.
		for _, r := range s.DepartureRunways {
			if ap, ok := sg.Airports[r.Airport]; ok && ap.DepartureController != "" {
				// If a virtual controller will take the initial track then
				// we don't need a human-controller to be covering it.
				continue
			}

			controller := ""
			for callsign, ctrl := range controllers {
				if ctrl.IsDepartureController(r.Airport, r.Runway) {
					if controller != "" {
						e.ErrorString("both \"%s\" and \"%s\" expect to handle %s/%s departures",
							controller, callsign, r.Airport, r.Runway)
					}
					controller = callsign
				}
			}
			if controller == "" {
				e.ErrorString("no controller found that is covering %s/%s departures",
					r.Airport, r.Runway)
			}
		}

		// Make sure all controllers are either the primary or have a path
		// of backup controllers that eventually ends with the primary.
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

			bmc, ok := controllers[mc.BackupController]
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
		for callsign, mc := range controllers {
			followPathToPrimary(callsign, mc, 25)
		}
		e.Pop()
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

			// For each multi-controller split, sure some controller covers the
			// arrival group.
			for split, controllers := range s.SplitConfigurations {
				e.Push("\"multi_controllers\": split \"" + split + "\"")
				count := 0
				for _, mc := range controllers {
					if idx := Find(mc.Arrivals, name); idx != -1 {
						count++
					}
				}
				if count == 0 {
					e.ErrorString("no controller in \"multi_controllers\" has this arrival group in their \"arrivals\"")
				} else if count > 1 {
					e.ErrorString("more than one controller in \"multi_controllers\" has this arrival group in their \"arrivals\"")
				}
				e.Pop()
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

var (
	// "FIX@HDG/DIST"
	reFixHeadingDistance = regexp.MustCompile(`^([\w]{3,})@([\d]{3})/(\d+(\.\d+)?)$`)
)

func (sg *ScenarioGroup) PostDeserialize(e *ErrorLogger, simConfigurations map[string]*SimConfiguration) {
	// Do these first!
	sg.Fixes = make(map[string]Point2LL)
	for _, fix := range sg.FixesStrings.Keys() {
		loc, _ := sg.FixesStrings.Get(fix)
		location, ok := loc.(string)
		if !ok {
			e.ErrorString("location for fix \"%s\" is not a string: %+v", fix, loc)
			continue
		}

		fix := strings.ToUpper(fix)
		e.Push("Fix  " + fix)

		if _, ok := sg.Fixes[fix]; ok {
			e.ErrorString("fix has multiple definitions")
		} else if pos, ok := sg.locate(location); ok {
			// It's something simple, likely a latlong that we could parse
			// directly.
			sg.Fixes[fix] = pos
		} else if strs := reFixHeadingDistance.FindStringSubmatch(location); len(strs) >= 4 {
			// "FIX@HDG/DIST"
			//fmt.Printf("A loc %s -> strs %+v\n", location, strs)
			if pll, ok := sg.locate(strs[1]); !ok {
				e.ErrorString("base fix \"" + strs[1] + "\" unknown")
			} else if hdg, err := strconv.Atoi(strs[2]); err != nil {
				e.ErrorString("heading \"%s\": %v", strs[2], err)
			} else if dist, err := strconv.ParseFloat(strs[3], 32); err != nil {
				e.ErrorString("distance \"%s\": %v", strs[3], err)
			} else {
				// Offset along the given heading and distance from the fix.
				p := ll2nm(pll, sg.NmPerLongitude)
				h := radians(float32(hdg))
				v := [2]float32{sin(h), cos(h)}
				v = scale2f(v, float32(dist))
				p = add2f(p, v)
				sg.Fixes[fix] = nm2ll(p, sg.NmPerLongitude)
			}
		} else {
			e.ErrorString("invalid location syntax \"%s\" for fix \"%s\"", location, fix)
		}

		e.Pop()
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
		if p, ok := sg.locate(rs.PositionString); rs.PositionString == "" || !ok {
			e.ErrorString("radar site position \"%s\" not found", rs.PositionString)
		} else {
			rs.Position = p
		}
		if rs.Char == "" {
			e.ErrorString("radar site is missing \"char\"")
		}
		if rs.Elevation == 0 {
			e.ErrorString("radar site is missing \"elevation\"")
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

				ar.Waypoints.CheckArrival(e)

				for rwy, wp := range ar.RunwayWaypoints {
					e.Push("Runway " + rwy)
					sg.InitializeWaypointLocations(wp, e)

					if wp[0].Fix != ar.Waypoints[len(ar.Waypoints)-1].Fix {
						e.ErrorString("initial \"runway_waypoints\" fix must match " +
							"last \"waypoints\" fix")
					}

					// For the check, splice together the last common
					// waypoint and the runway waypoints.  This will give
					// us a repeated first fix, but this way we can check
					// compliance with restrictions at that fix...
					ewp := append([]Waypoint{ar.Waypoints[len(ar.Waypoints)-1]}, wp...)
					WaypointArray(ewp).CheckArrival(e)

					e.Pop()
				}
			}

			for arrivalAirport, airlines := range ar.Airlines {
				e.Push("Arrival airport " + arrivalAirport)
				for _, al := range airlines {
					database.CheckAirline(al.ICAO, al.Fleet, e)
					if _, ok := database.Airports[al.Airport]; !ok {
						e.ErrorString("departure airport \"airport\" \"%s\" unknown", al.Airport)
					}
				}

				ap, ok := sg.Airports[arrivalAirport]
				if !ok {
					e.ErrorString("arrival airport \"%s\" unknown", arrivalAirport)
				} else if ar.ExpectApproach != "" {
					if _, ok := ap.Approaches[ar.ExpectApproach]; !ok {
						e.ErrorString("arrival airport \"%s\" doesn't have a \"%s\" approach",
							arrivalAirport, ar.ExpectApproach)
					}
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

	for _, rp := range sg.ReportingPointStrings {
		if loc, ok := sg.locate(rp); !ok {
			e.ErrorString("unknown \"reporting_point\" \"%s\"", rp)
		} else {
			sg.ReportingPoints = append(sg.ReportingPoints, ReportingPoint{Fix: rp, Location: loc})
		}
	}

	// Do after airports!
	for name, s := range sg.Scenarios {
		e.Push("Scenario " + name)
		s.PostDeserialize(sg, e)
		e.Pop()
	}

	initializeSimConfigurations(sg, simConfigurations, *server)
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
			SplitConfigurations: scenario.SplitConfigurations,
			LaunchConfig: MakeLaunchConfig(scenario.DepartureRunways,
				scenario.ArrivalGroupDefaultRates),
			Wind:             scenario.Wind,
			DepartureRunways: scenario.DepartureRunways,
			ArrivalRunways:   scenario.ArrivalRunways,
		}

		if multiController {
			if len(scenario.SplitConfigurations) == 0 {
				// not a multi-controller scenario
				continue
			}
			sc.SelectedController = scenario.SplitConfigurations.GetPrimaryController(scenario.DefaultSplit)
			sc.SelectedSplit = scenario.DefaultSplit
		} else {
			if scenario.SoloController == "" {
				// multi-controller only
				continue
			}
			sc.SelectedController = scenario.SoloController
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

	// Do DME arcs after wp.Locations have been initialized
	for i, wp := range waypoints {
		if wp.Arc == nil {
			continue
		}

		if e != nil {
			e.Push("Fix " + wp.Fix)
		}

		if pos, ok := sg.locate(wp.Arc.Fix); !ok {
			if e != nil {
				e.ErrorString("unable to locate arc center \"" + wp.Arc.Fix + "\"")
			}
		} else {
			wp.Arc.Center = pos
		}

		if i+1 == len(waypoints) {
			if e != nil {
				e.ErrorString("can't have DME arc after final waypoint")
			}
		} else {
			hpre := headingp2ll(wp.Arc.Center, waypoints[i].Location, 60 /* nm per */, 0 /* mag */)
			hpost := headingp2ll(wp.Arc.Center, waypoints[i+1].Location, 60 /* nm per */, 0 /* mag */)

			h := NormalizeHeading(hpost - hpre)
			wp.Arc.Clockwise = h < 180
		}

		// Heading from the center of the arc to the current fix
		hfix := headingp2ll(wp.Arc.Center, wp.Location,
			sg.NmPerLongitude, sg.MagneticVariation)
		// Then perpendicular to that, depending on the arc's direction
		wp.Arc.InitialHeading = NormalizeHeading(hfix + float32(Select(wp.Arc.Clockwise, 90, -90)))

		if e != nil {
			e.Pop()
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// Airspace

func InAirspace(p Point2LL, alt float32, volumes []ControllerAirspaceVolume) (bool, [][2]int) {
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

type LoadedVideoMap struct {
	path        string
	commandBufs map[string]CommandBuffer
	err         error
}

func loadVideoMaps(filesystem fs.FS, path string, result chan LoadedVideoMap) {
	start := time.Now()
	lvm := LoadedVideoMap{path: path}

	contents, err := fs.ReadFile(filesystem, path)
	if err != nil {
		lvm.err = err
		result <- lvm
		return
	}

	if strings.HasSuffix(strings.ToLower(path), ".zst") {
		contents = []byte(decompressZstd(string(contents)))
	}

	lvm.commandBufs, err = loadVideoMapFile(contents)
	if err != nil {
		lvm.err = err
		result <- lvm
		return
	}

	lg.Infof("%s: video map loaded in %s\n", path, time.Since(start))

	result <- lvm
}

func loadVideoMapFile(b []byte) (map[string]CommandBuffer, error) {
	// For debugging, enable check here; the file will also be parsed using
	// encoding/json and the result will be compared to what we get out of
	// our parser.
	check := false
	var checkJSONMaps map[string][]Point2LL
	if check {
		if err := UnmarshalJSON(b, &checkJSONMaps); err != nil {
			return nil, err
		}
	}

	// Custom "just enough" JSON parser below. This expects only vice JSON
	// video map files but is ~2x faster than using encoding/json.
	pos := 0 // byte offset into the file
	line := 1
	skipWhitespace := func() {
		for pos < len(b) {
			if b[pos] == '\n' {
				line++
			}
			if b[pos] != ' ' && b[pos] != '\n' && b[pos] != '\t' && b[pos] != '\f' && b[pos] != '\r' && b[pos] != '\v' {
				break
			}
			pos++
		}
	}
	// Called when we expect the given character as the next token.
	expect := func(ch byte) error {
		skipWhitespace()
		if pos < len(b) && b[pos] == ch {
			pos++
			return nil
		}
		return fmt.Errorf("expected '%s' at line %d, found '%s'", string(ch), line, string(b[pos]))
	}
	// tryQuoted tries to return a quoted string; nil is returned if the
	// first non-whitespace character found is not a quotation mark.
	tryQuoted := func() []byte {
		skipWhitespace()
		if pos < len(b) && b[pos] != '"' {
			return nil
		}
		pos++

		// Scan ahead to the closing quote
		start := pos
		for pos < len(b) && b[pos] != '"' {
			if b[pos] == '\n' {
				panic(fmt.Sprintf("unterminated string at line %d", line))
			}
			pos++
		}
		if pos == len(b) {
			panic("unterminated string")
		}
		pos++ // skip closing quote
		return b[start : pos-1]
	}
	// tryComma returns true and advances pos if the next non-whitespace
	// character is a comma.
	tryComma := func() bool {
		skipWhitespace()
		ok := pos < len(b) && b[pos] == ','
		if ok {
			pos++
		}
		return ok
	}

	tryNull := func() bool {
		skipWhitespace()
		if pos+3 < len(b) && string(b[pos:pos+4]) == "null" {
			pos += 4
			return true
		}
		return false
	}

	m := make(map[string]CommandBuffer)

	// Video map JSON files encode a JSON object where members are arrays
	// of strings, where each string encodes a lat-long position.
	if err := expect('{'); err != nil {
		return nil, err
	}
	for {
		// Is there another member in the object?
		name := tryQuoted()
		if len(name) == 0 {
			break
		}
		if err := expect(':'); err != nil {
			return nil, err
		}

		// Expect an array for its value.
		var segs []Point2LL
		// Allow "null" for an empty array but ignore it
		if tryNull() {
			// don't try to parse the array...
		} else {
			if err := expect('['); err != nil {
				return nil, err
			}

			for {
				// Parse an element of the array, which should be a string
				// representing a position.
				ll := tryQuoted()
				if len(ll) == 0 {
					break
				}

				p, err := ParseLatLong(ll)
				if err != nil {
					return nil, err
				}
				segs = append(segs, p)

				// Is there another entry after this one?
				if !tryComma() {
					break
				}
			}
			// Array close.
			if err := expect(']'); err != nil {
				return nil, err
			}

			if check {
				// Make sure we have the same number of points and that they
				// are equal to the reference deserialized by encoding/json.
				jsegs, ok := checkJSONMaps[string(name)]
				if !ok {
					return nil, fmt.Errorf("%s: not found in encoding/json deserialized maps", string(name))
				}
				if len(jsegs) != len(segs) {
					return nil, fmt.Errorf("%s: encoding/json returned %d segments, we found %d", string(name), len(jsegs), len(segs))
				}
				for i := range jsegs {
					if jsegs[i][0] != segs[i][0] || jsegs[i][1] != segs[i][1] {
						return nil, fmt.Errorf("%s: %d'th point mismatch: encoding/json %v ours %v", string(name), i, jsegs[i], segs[i])
					}
				}
				delete(checkJSONMaps, string(name))
			}
		}

		// Generate the command buffer to draw this video map.
		ld := GetLinesDrawBuilder()

		for i := 0; i < len(segs)/2; i++ {
			ld.AddLine(segs[2*i], segs[2*i+1])
		}
		var cb CommandBuffer
		ld.GenerateCommands(&cb)

		m[string(name)] = cb
		ReturnLinesDrawBuilder(ld)

		// Is there another video map in the object?
		if !tryComma() {
			break
		}
	}
	expect('}')

	if check && len(checkJSONMaps) > 0 {
		var s []string
		for k := range checkJSONMaps {
			s = append(s, k)
		}
		return nil, fmt.Errorf("encoding/json found maps that we did not: %s", strings.Join(s, " "))
	}

	return m, nil
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

	start := time.Now()
	vmChan := make(chan LoadedVideoMap, 16)
	launches := 0

	err := fs.WalkDir(resourcesFS, "videomaps", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			lg.Errorf("error walking videomaps: %v", err)
			return nil
		}

		if d.IsDir() {
			return nil
		}

		if filepath.Ext(path) != ".json" && filepath.Ext(path) != ".zst" {
			return nil
		}

		launches++
		go loadVideoMaps(resourcesFS, path, vmChan)
		return nil
	})
	if err != nil {
		lg.Errorf("error loading videomaps: %v", err)
		os.Exit(1)
	}

	receiveLoadedVideoMap := func() {
		lvm := <-vmChan
		if lvm.err != nil {
			e.Push("File " + lvm.path)
			e.Error(lvm.err)
			e.Pop()
		} else {
			videoMapCommandBuffers[lvm.path] = lvm.commandBufs
		}
	}

	// Get all of the loaded video map command buffers
	for launches > 0 {
		receiveLoadedVideoMap()
		launches--
	}

	lg.Infof("video map load time: %s\n", time.Since(start))

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
			loadVideoMaps(fs, filename, vmChan)
			receiveLoadedVideoMap()
		}
	}
	loadVid(*videoMapFilename)
	if globalConfig != nil {
		loadVid(globalConfig.DevVideoMapFile)
	}

	// Now load the scenarios.
	scenarioGroups := make(map[string]*ScenarioGroup)
	simConfigurations := make(map[string]*SimConfiguration)

	err = fs.WalkDir(resourcesFS, "scenarios", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			lg.Errorf("error walking scenarios/: %v", err)
			return nil
		}

		if d.IsDir() {
			return nil
		}

		if filepath.Ext(path) != ".json" {
			return nil
		}

		lg.Infof("%s: loading scenario", path)
		s := loadScenarioGroup(resourcesFS, path, e)
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
		lg.Errorf("error walking scenarios: %v", err)
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

	// Walk all of the scenario groups to get all of the possible departing aircraft
	// types to see where V2 is needed in the performance database..
	acTypes := make(map[string]struct{})
	for _, sg := range scenarioGroups {
		for _, ap := range sg.Airports {
			for _, dep := range ap.Departures {
				for _, al := range dep.Airlines {
					fleet := Select(al.Fleet != "", al.Fleet, "default")
					for _, ac := range database.Airlines[al.ICAO].Fleets[fleet] {
						acTypes[ac.ICAO] = struct{}{}
					}
				}
			}
		}
	}
	var missing []string
	for _, t := range SortedMapKeys(acTypes) {
		if database.AircraftPerformance[t].Speed.V2 == 0 {
			missing = append(missing, t)
		}
	}
	lg.Warnf("Missing V2 in performance database: %s", strings.Join(missing, ", "))

	return scenarioGroups, simConfigurations
}

///////////////////////////////////////////////////////////////////////////
// SplitConfigurations

func (sc SplitConfigurationSet) GetConfiguration(split string) SplitConfiguration {
	if len(sc) == 1 {
		// ignore split
		for _, config := range sc {
			return config
		}
	}

	config, ok := sc[split]
	if !ok {
		lg.Error("split not found: \""+split+"\"", slog.Any("splits", sc))
	}
	return config
}

func (sc SplitConfigurationSet) GetPrimaryController(split string) string {
	for callsign, mc := range sc.GetConfiguration(split) {
		if mc.Primary {
			return callsign
		}
	}

	lg.Error("No primary in split: \""+split+"\"", slog.Any("splits", sc))
	return ""
}

func (sc SplitConfigurationSet) Len() int {
	return len(sc)
}

func (sc SplitConfigurationSet) Splits() []string {
	return SortedMapKeys(sc)
}

///////////////////////////////////////////////////////////////////////////
// SplitConfiguration

// ResolveController takes a controller callsign and returns the signed-in
// controller that is responsible for that position (possibly just the
// provided callsign).
func (sc SplitConfiguration) ResolveController(callsign string, active func(callsign string) bool) string {
	i := 0
	for {
		if active(callsign) {
			return callsign
		}

		if ctrl, ok := sc[callsign]; !ok {
			lg.Errorf("%s: failed to find controller in MultiControllers", callsign)
			return ""
		} else {
			callsign = ctrl.BackupController
		}

		i++
		if i == 20 {
			lg.Errorf("%s: unable to find backup for arrival handoff controller", callsign)
			return ""
		}
	}
}

func (sc SplitConfiguration) GetArrivalController(arrivalGroup string) string {
	for callsign, ctrl := range sc {
		if ctrl.IsArrivalController(arrivalGroup) {
			return callsign
		}
	}

	lg.Error(arrivalGroup+": couldn't find arrival controller", slog.Any("config", sc))
	return ""
}

func (sc SplitConfiguration) GetDepartureController(airport, runway string) string {
	for callsign, ctrl := range sc {
		if ctrl.IsDepartureController(airport, runway) {
			return callsign
		}
	}

	lg.Error(airport+"/"+runway+": couldn't find departure controller", slog.Any("config", sc))
	return ""
}

///////////////////////////////////////////////////////////////////////////
// MultiUserController

func (c *MultiUserController) IsDepartureController(ap, rwy string) bool {
	for _, d := range c.Departures {
		depAirport, depRunway, ok := strings.Cut(d, "/")
		if ok { // have a runway
			if ap == depAirport && rwy == depRunway {
				return true
			}
		} else { // no runway, only match airport
			if ap == depAirport {
				return true
			}
		}
	}
	return false
}

func (c *MultiUserController) IsArrivalController(arrivalGroup string) bool {
	return Find(c.Arrivals, arrivalGroup) != -1
}

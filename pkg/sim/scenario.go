// pkg/sim/scenario.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iancoleman/orderedmap"
	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/util"
)

type ScenarioGroup struct {
	TRACON           string                    `json:"tracon"`
	Name             string                    `json:"name"`
	Airports         map[string]*av.Airport    `json:"airports"`
	Fixes            map[string]math.Point2LL  `json:"-"`
	FixesStrings     orderedmap.OrderedMap     `json:"fixes"`
	Scenarios        map[string]*Scenario      `json:"scenarios"`
	DefaultScenario  string                    `json:"default_scenario"`
	ControlPositions map[string]*av.Controller `json:"control_positions"`
	Airspace         Airspace                  `json:"airspace"`
	InboundFlows     map[string]InboundFlow    `json:"inbound_flows"`

	// Temporary for the transition to inbound_flows
	ArrivalGroups map[string][]av.Arrival `json:"arrival_groups"`

	PrimaryAirport string `json:"primary_airport"`

	ReportingPointStrings []string            `json:"reporting_points"`
	ReportingPoints       []av.ReportingPoint // not in JSON

	NmPerLatitude           float32 // Always 60
	NmPerLongitude          float32 // Derived from Center
	MagneticVariation       float32
	MagneticAdjustment      float32                 `json:"magnetic_adjustment"`
	STARSFacilityAdaptation STARSFacilityAdaptation `json:"stars_config"`
}

type InboundFlow struct {
	Arrivals    []av.Arrival    `json:"arrivals"`
	Overflights []av.Overflight `json:"overflights"`
}

type AirspaceAwareness struct {
	Fix                 []string `json:"fixes"`
	AltitudeRange       [2]int   `json:"altitude_range"`
	ReceivingController string   `json:"receiving_controller"`
	AircraftType        []string `json:"aircraft_type"`
}

type STARSFacilityAdaptation struct {
	AirspaceAwareness   []AirspaceAwareness              `json:"airspace_awareness"`
	ForceQLToSelf       bool                             `json:"force_ql_self"`
	AllowLongScratchpad bool                             `json:"allow_long_scratchpad"`
	VideoMapNames       []string                         `json:"stars_maps"`
	VideoMapLabels      map[string]string                `json:"map_labels"`
	ControllerConfigs   map[string]STARSControllerConfig `json:"controller_configs"`
	InhibitCAVolumes    []av.AirspaceVolume              `json:"inhibit_ca_volumes"`
	RadarSites          map[string]*av.RadarSite         `json:"radar_sites"`
	Center              math.Point2LL                    `json:"-"`
	CenterString        string                           `json:"center"`
	Range               float32                          `json:"range"`
	Scratchpads         map[string]string                `json:"scratchpads"`
	VideoMapFile        string                           `json:"video_map_file"`
	CoordinationFixes   map[string]av.AdaptationFixes    `json:"coordination_fixes"`
	SingleCharAIDs      map[string]string                `json:"single_char_aids"` // Char to airport
	BeaconBank          int                              `json:"beacon_bank"`
	KeepLDB             bool                             `json:"keep_ldb"`
	PDB                 struct {
		ShowScratchpad2  bool `json:"show_scratchpad2"`
		HideGroundspeed  bool `json:"hide_gs"`
		ShowAircraftType bool `json:"show_aircraft_type"`
		SplitGSAndCWT    bool `json:"split_gs_and_cwt"`
	} `json:"pdb"`
}

type STARSControllerConfig struct {
	VideoMapNames []string      `json:"video_maps"`
	DefaultMaps   []string      `json:"default_maps"`
	Center        math.Point2LL `json:"-"`
	CenterString  string        `json:"center"`
	Range         float32       `json:"range"`
}

type Airspace struct {
	Boundaries map[string][]math.Point2LL            `json:"boundaries"`
	Volumes    map[string][]ControllerAirspaceVolume `json:"volumes"`
}

type ControllerAirspaceVolume struct {
	LowerLimit    int               `json:"lower"`
	UpperLimit    int               `json:"upper"`
	Boundaries    [][]math.Point2LL `json:"boundary_polylines"` // not in JSON
	BoundaryNames []string          `json:"boundaries"`
}

type Scenario struct {
	SoloController      string                   `json:"solo_controller"`
	SplitConfigurations av.SplitConfigurationSet `json:"multi_controllers"`
	DefaultSplit        string                   `json:"default_split"`
	Wind                av.Wind                  `json:"wind"`
	VirtualControllers  []string                 `json:"controllers"`

	// Map from inbound flow names to a map from airport name to default rate,
	// with "overflights" a special case to denote overflights
	InboundFlowDefaultRates map[string]map[string]int `json:"inbound_rates"`
	// Temporary backwards compatibility
	ArrivalGroupDefaultRates map[string]map[string]int `json:"arrivals"`

	ApproachAirspace       []ControllerAirspaceVolume `json:"approach_airspace_volumes"`  // not in JSON
	DepartureAirspace      []ControllerAirspaceVolume `json:"departure_airspace_volumes"` // not in JSON
	ApproachAirspaceNames  []string                   `json:"approach_airspace"`
	DepartureAirspaceNames []string                   `json:"departure_airspace"`

	DepartureRunways []ScenarioGroupDepartureRunway `json:"departure_runways,omitempty"`
	ArrivalRunways   []ScenarioGroupArrivalRunway   `json:"arrival_runways,omitempty"`

	Center       math.Point2LL `json:"-"`
	CenterString string        `json:"center"`
	Range        float32       `json:"range"`
	DefaultMaps  []string      `json:"default_maps"`
}

type ScenarioGroupDepartureRunway struct {
	Airport     string `json:"airport"`
	Runway      string `json:"runway"`
	Category    string `json:"category,omitempty"`
	DefaultRate int    `json:"rate"`

	ExitRoutes map[string]av.ExitRoute // copied from airport's  departure_routes
}

type ScenarioGroupArrivalRunway struct {
	Airport string `json:"airport"`
	Runway  string `json:"runway"`
}

func (s *Scenario) PostDeserialize(sg *ScenarioGroup, e *util.ErrorLogger) {
	// Temporary backwards-compatibility for inbound flows
	if len(s.ArrivalGroupDefaultRates) > 0 {
		if len(s.InboundFlowDefaultRates) > 0 {
			e.ErrorString("cannot specify both \"arrivals\" and \"inbound_rates\"")
		} else {
			s.InboundFlowDefaultRates = s.ArrivalGroupDefaultRates
			s.ArrivalGroupDefaultRates = nil
		}
	}
	for name, controllers := range s.SplitConfigurations {
		e.Push("\"multi_controllers\": split \"" + name + "\"")
		for _, ctrl := range controllers {
			if len(ctrl.Arrivals) > 0 {
				if len(ctrl.InboundFlows) > 0 {
					e.ErrorString("cannot specify both \"arrivals\" and \"inbound_flows\"")
				} else {
					ctrl.InboundFlows = ctrl.Arrivals
					ctrl.Arrivals = nil
				}
			}
		}
		e.Pop()
	}

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

	airportExits := make(map[string]map[string]interface{}) // airport -> exit -> is it covered
	for i, rwy := range s.DepartureRunways {
		e.Push("Departure runway " + rwy.Airport + " " + rwy.Runway)

		if airportExits[rwy.Airport] == nil {
			airportExits[rwy.Airport] = make(map[string]interface{})
		}

		if ap, ok := sg.Airports[rwy.Airport]; !ok {
			e.ErrorString("airport not found")
		} else {
			if routes, ok := ap.DepartureRoutes[rwy.Runway]; !ok {
				e.ErrorString("runway departure routes not found")
			} else {
				s.DepartureRunways[i].ExitRoutes = routes
				for exit := range routes {
					// It's fine if multiple active runways cover the exit.
					airportExits[rwy.Airport][exit] = nil
				}
			}

			if len(ap.Departures) == 0 {
				e.ErrorString("no \"departures\" specified for airport")
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
	for icao, exits := range airportExits {
		// We already gave an error above if the airport is unknown, so
		// don't need to again here..
		if ap, ok := sg.Airports[icao]; ok {
			for _, dep := range ap.Departures {
				if _, ok := exits[dep.Exit]; !ok {
					e.ErrorString("No active runway at %s covers in-use exit \"%s\"", icao, dep.Exit)
				}
			}
		}
	}

	// Make sure all of the controllers used in airspace awareness will be there.
	for _, aa := range sg.STARSFacilityAdaptation.AirspaceAwareness {
		if !slices.Contains(s.VirtualControllers, aa.ReceivingController) {
			s.VirtualControllers = append(s.VirtualControllers, aa.ReceivingController)
		}
	}

	sort.Slice(s.ArrivalRunways, func(i, j int) bool {
		if s.ArrivalRunways[i].Airport == s.ArrivalRunways[j].Airport {
			return s.ArrivalRunways[i].Runway < s.ArrivalRunways[j].Runway
		}
		return s.ArrivalRunways[i].Airport < s.ArrivalRunways[j].Airport
	})

	activeAirports := make(map[*av.Airport]interface{}) // all airports with departures or arrivals
	for _, rwy := range s.ArrivalRunways {
		e.Push("Arrival runway " + rwy.Airport + " " + rwy.Runway)

		if ap, ok := sg.Airports[rwy.Airport]; !ok {
			e.ErrorString("airport not found")
		} else {
			activeAirports[ap] = nil

			found := false
			for _, appr := range ap.Approaches {
				if appr.Runway == rwy.Runway {
					found = true
					// Add the tower controller to the virtual controller
					// list if it isn't there already.
					if !slices.Contains(s.VirtualControllers, appr.TowerController) {
						s.VirtualControllers = append(s.VirtualControllers, appr.TowerController)
					}
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

	// Figure out which airports/runways and airports/SIDs are used in the scenario.
	activeAirportSIDs := make(map[string]map[string]interface{})
	activeAirportRunways := make(map[string]map[string]interface{})
	activeDepartureAirports := make(map[string]interface{})
	for _, rwy := range s.DepartureRunways {
		e.Push("departure runway " + rwy.Runway)

		ap, ok := sg.Airports[rwy.Airport]
		if !ok {
			e.ErrorString("%s: airport unknown", rwy.Airport)
		} else {
			activeAirports[ap] = nil
			activeDepartureAirports[rwy.Airport] = nil

			if ap.DepartureController == "" {
				// Only check for a human controller to be covering the track if there isn't
				// a virtual controller assigned to it.
				for fix, route := range rwy.ExitRoutes {
					if rwy.Category == "" || ap.ExitCategories[fix] == rwy.Category {
						if activeAirportSIDs[rwy.Airport] == nil {
							activeAirportSIDs[rwy.Airport] = make(map[string]interface{})
						}
						if activeAirportRunways[rwy.Airport] == nil {
							activeAirportRunways[rwy.Airport] = make(map[string]interface{})
						}
						activeAirportSIDs[rwy.Airport][route.SID] = nil
						activeAirportRunways[rwy.Airport][rwy.Runway] = nil
					}
				}
			}
		}

		e.Pop()
	}

	// Do any active airports have CRDA?
	haveCRDA := false
	for ap := range activeAirports {
		if len(ap.ConvergingRunways) > 0 {
			haveCRDA = true
			break
		}
	}
	if haveCRDA {
		// Make sure all of the controllers involved have a valid default airport
		check := func(ctrl *av.Controller) {
			if ctrl.DefaultAirport == "" {
				if ap, _, ok := strings.Cut(ctrl.Callsign, "_"); ok { // see if the first part of the callsign is an airport
					if _, ok := sg.Airports["K"+ap]; ok {
						ctrl.DefaultAirport = "K" + ap
					} else {
						e.ErrorString("%s: controller must have \"default_airport\" specified (required for CRDA).", ctrl.Callsign)
						return
					}
				}
			}
			if _, ok := sg.Airports[ctrl.DefaultAirport]; !ok {
				e.ErrorString("%s: controller's \"default_airport\" \"%s\" is unknown", ctrl.Callsign, ctrl.DefaultAirport)
			}
		}

		if ctrl, ok := sg.ControlPositions[s.SoloController]; ok {
			check(ctrl)
		}
		for _, callsign := range s.SplitConfigurations.Splits() {
			if ctrl, ok := sg.ControlPositions[callsign]; ok {
				check(ctrl)
			}
		}
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

		haveDepartureSIDSpec, haveDepartureRunwaySpec := false, false

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

			if _, ok := sg.ControlPositions[callsign]; !ok {
				e.ErrorString("controller \"%s\" not defined in the scenario group's \"control_positions\"", callsign)
			}

			// Make sure any airports claimed for departures are valid
			for _, airportSID := range ctrl.Departures {
				ap, sidRunway, haveSIDRunway := strings.Cut(airportSID, "/")
				if sids, ok := activeAirportSIDs[ap]; !ok {
					e.ErrorString("airport \"%s\" is not departing aircraft in this scenario", ap)
				} else if haveSIDRunway {
					// If there's something after a slash, make sure it's
					// either a valid SID or runway.
					_, okSID := sids[sidRunway]
					_, okRunway := activeAirportRunways[ap][sidRunway]
					if !okSID && !okRunway {
						e.ErrorString("\"%s\" at airport \"%s\" is neither an active runway or SID in this scenario", sidRunway, ap)
					}

					haveDepartureSIDSpec = haveDepartureSIDSpec || okSID
					haveDepartureRunwaySpec = haveDepartureRunwaySpec || okRunway
					if haveDepartureSIDSpec && haveDepartureRunwaySpec {
						e.ErrorString("cannot use both runways and SIDs to specify the departure controller")
					}
				}
			}

			// Make sure all inbound flows are valid. Below we make sure all
			// included arrivals have a controller.
			for _, flow := range ctrl.InboundFlows {
				if _, ok := s.InboundFlowDefaultRates[flow]; !ok {
					e.ErrorString("inbound flow \"%s\" not found in scenario", flow)
				} else {
					f := sg.InboundFlows[flow]
					overflightHasHandoff := func(of av.Overflight) bool {
						return slices.ContainsFunc(of.Waypoints, func(wp av.Waypoint) bool { return wp.Handoff })
					}
					if len(f.Arrivals) == 0 && !slices.ContainsFunc(f.Overflights, overflightHasHandoff) {
						// It's just overflights without handoffs
						e.ErrorString("no inbound flows in \"%s\" have handoffs", flow)
					}
				}
			}
			e.Pop()
		}
		if primaryController == "" {
			e.ErrorString("No controller in \"multi_controllers\" was specified as \"primary\"")
		}

		// Make sure each active departure config (airport and possibly
		// SID) has exactly one controller handling its departures.
		validateDep := func(active map[string]map[string]interface{}, check func(ctrl *av.MultiUserController, airport, spec string) bool) {
			for airport, specs := range active {
				for spec := range specs {
					controller := ""
					for callsign, ctrl := range controllers {
						if check(ctrl, airport, spec) {
							if controller != "" {
								e.ErrorString("both %s and %s expect to handle %s/%s departures",
									controller, callsign, airport, spec)
							}
							controller = callsign
						}
					}
					if controller == "" {
						e.ErrorString("no controller found that is covering %s/%s departures", airport, spec)
					}
				}
			}
		}
		if haveDepartureSIDSpec {
			validateDep(activeAirportSIDs, func(ctrl *av.MultiUserController, airport, spec string) bool {
				return ctrl.IsDepartureController(airport, "", spec)
			})
		} else if haveDepartureRunwaySpec {
			validateDep(activeAirportRunways, func(ctrl *av.MultiUserController, airport, spec string) bool {
				return ctrl.IsDepartureController(airport, spec, "")
			})
		} else {
			// Just airports
			for airport := range activeDepartureAirports {
				if sg.Airports[airport].DepartureController != "" {
					// It's covered by a virtual controller
					continue
				}

				controller := ""
				for callsign, ctrl := range controllers {
					if ctrl.IsDepartureController(airport, "", "") {
						if controller != "" {
							e.ErrorString("both %s and %s expect to handle %s departures",
								controller, callsign, airport)
						}
						controller = callsign
					}
				}
				if controller == "" {
					e.ErrorString("no controller found that is covering %s departures", airport)
				}
			}
		}

		// Make sure all controllers are either the primary or have a path
		// of backup controllers that eventually ends with the primary.
		havePathToPrimary := make(map[string]interface{})
		havePathToPrimary[primaryController] = nil
		var followPathToPrimary func(callsign string, mc *av.MultiUserController, depth int) bool
		followPathToPrimary = func(callsign string, mc *av.MultiUserController, depth int) bool {
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
			if !followPathToPrimary(callsign, mc, 25) {
				e.ErrorString("controller \"%s\" doesn't have a valid backup controller", callsign)
			}
		}
		e.Pop()
	}

	for _, name := range util.SortedMapKeys(s.InboundFlowDefaultRates) {
		e.Push("Inbound flow " + name)
		// Make sure the inbound flow has been defined
		if flow, ok := sg.InboundFlows[name]; !ok {
			e.ErrorString("inbound flow not found")
		} else {
			// Add initial controllers to the controller list, if
			// necessary.
			for _, ar := range flow.Arrivals {
				if ar.InitialController != "" &&
					!slices.Contains(s.VirtualControllers, ar.InitialController) {
					s.VirtualControllers = append(s.VirtualControllers, ar.InitialController)
				}
			}
			for _, of := range flow.Overflights {
				if of.InitialController != "" &&
					!slices.Contains(s.VirtualControllers, of.InitialController) {
					s.VirtualControllers = append(s.VirtualControllers, of.InitialController)
				}
			}

			// Check the airports in it
			for category := range s.InboundFlowDefaultRates[name] {
				if category == "overflights" {
					if len(flow.Overflights) == 0 {
						e.ErrorString("Rate specified for \"overflights\" but no overflights specified in \"%s\"", name)
					}
				} else {
					airport := category
					e.Push("Airport " + airport)
					if _, ok := sg.Airports[airport]; !ok {
						e.ErrorString("unknown arrival airport")
					} else {
						// Make sure the airport exists in at least one of the
						// arrivals in the group.
						found := false
						for _, ar := range flow.Arrivals {
							if _, ok := ar.Airlines[airport]; ok {
								found = true

								// Make sure the airport has at least one
								// active arrival runway.
								if !slices.ContainsFunc(s.ArrivalRunways,
									func(r ScenarioGroupArrivalRunway) bool {
										return r.Airport == airport
									}) {
									e.ErrorString("no runways listed in \"arrival_runways\" for %s even though there are %s arrivals in \"arrivals\"",
										airport, airport)
								}
							}
						}
						if !found {
							e.ErrorString("airport not used for any arrivals")
						}
					}
					e.Pop()
				}
			}

			// For each multi-controller split, sure some controller covers the
			// flow if there will be a handoff to a non-virtual controller.
			hasHandoff := len(flow.Arrivals) > 0
			for _, of := range flow.Overflights {
				if slices.ContainsFunc(of.Waypoints, func(wp av.Waypoint) bool { return wp.Handoff }) {
					hasHandoff = true
				}
			}
			if hasHandoff {
				for split, controllers := range s.SplitConfigurations {
					e.Push("\"multi_controllers\": split \"" + split + "\"")
					count := 0
					for _, mc := range controllers {
						if slices.Contains(mc.InboundFlows, name) {
							count++
						}
					}
					if count == 0 {
						e.ErrorString("no controller in \"multi_controllers\" has \"%s\" in their \"inbound_flows\"", name)
					} else if count > 1 {
						e.ErrorString("more than one controller in \"multi_controllers\" has this in their \"inbound_flows\"")
					}
					e.Pop()
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

	if s.CenterString != "" {
		if pos, ok := sg.Locate(s.CenterString); !ok {
			e.ErrorString("unknown location \"%s\" specified for \"center\"", s.CenterString)
		} else {
			s.Center = pos
		}
	}

	fa := sg.STARSFacilityAdaptation
	if len(s.DefaultMaps) > 0 {
		if len(fa.ControllerConfigs) > 0 {
			e.ErrorString("\"default_maps\" is not allowed when \"controller_configs\" is set in \"stars_config\"")
		}

		for _, dm := range s.DefaultMaps {
			if !slices.ContainsFunc(fa.VideoMapNames,
				func(m string) bool { return m == dm }) {
				e.ErrorString("video map \"%s\" not found in \"stars_maps\"", dm)
			}
		}
	} else if len(fa.ControllerConfigs) > 0 {
		// Make sure that each controller in the scenario is represented in
		// "controller_configs".
		if _, ok := fa.ControllerConfigs[s.SoloController]; !ok {
			e.ErrorString("control position \"%s\" is not included in \"controller_configs\"",
				s.SoloController)
		}
		for _, conf := range s.SplitConfigurations {
			for pos := range conf {
				if _, ok := fa.ControllerConfigs[pos]; !ok {
					e.ErrorString("control position \"%s\" is not included in \"controller_configs\"", pos)
				}
			}
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// ScenarioGroup

func (sg *ScenarioGroup) Locate(s string) (math.Point2LL, bool) {
	s = strings.ToUpper(s)
	// ScenarioGroup's definitions take precedence...
	if p, ok := sg.Fixes[s]; ok {
		return p, true
	} else if n, ok := av.DB.Navaids[strings.ToUpper(s)]; ok {
		return n.Location, ok
	} else if ap, ok := av.DB.Airports[strings.ToUpper(s)]; ok {
		return ap.Location, ok
	} else if f, ok := av.DB.Fixes[strings.ToUpper(s)]; ok {
		return f.Location, ok
	} else if p, err := math.ParseLatLong([]byte(s)); err == nil {
		return p, true
	} else if len(s) > 5 && s[0] == 'K' && s[4] == '-' {
		if rwy, ok := av.LookupRunway(s[:4], s[5:]); ok {
			return rwy.Threshold, true
		}
	}

	return math.Point2LL{}, false
}

var (
	// "FIX@HDG/DIST"
	reFixHeadingDistance = regexp.MustCompile(`^([\w-]{3,})@([\d]{3})/(\d+(\.\d+)?)$`)
)

func (sg *ScenarioGroup) PostDeserialize(multiController bool, e *util.ErrorLogger, simConfigurations map[string]map[string]*Configuration) {
	// Temporary backwards compatibility for inbound flows
	if len(sg.ArrivalGroups) > 0 {
		if len(sg.InboundFlows) > 0 {
			e.ErrorString("cannot specify both \"arrival_groups\" and \"inbound_flows\"")
		} else {
			sg.InboundFlows = make(map[string]InboundFlow)
			for name, arrivals := range sg.ArrivalGroups {
				var flow InboundFlow
				for _, ar := range arrivals {
					flow.Arrivals = append(flow.Arrivals, ar)
				}
				sg.InboundFlows[name] = flow
			}
			sg.ArrivalGroups = nil
		}
	}
	// stars_config items. This goes first because we need to initialize
	// Center (and thence NmPerLongitude) ASAP.
	sg.STARSFacilityAdaptation.PostDeserialize(e, sg)

	sg.NmPerLatitude = 60
	sg.NmPerLongitude = 60 * math.Cos(math.Radians(sg.STARSFacilityAdaptation.Center[1]))

	if sg.TRACON == "" {
		e.ErrorString("\"tracon\" must be specified")
	} else if _, ok := av.DB.TRACONs[sg.TRACON]; !ok {
		e.ErrorString("TRACON %s is unknown; it must be a 3-letter identifier listed at "+
			"https://www.faa.gov/about/office_org/headquarters_offices/ato/service_units/air_traffic_services/tracon.",
			sg.TRACON)
	}

	sg.Fixes = make(map[string]math.Point2LL)
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
		} else if strs := reFixHeadingDistance.FindStringSubmatch(location); len(strs) >= 4 {
			// "FIX@HDG/DIST"
			//fmt.Printf("A loc %s -> strs %+v\n", location, strs)
			if pll, ok := sg.Locate(strs[1]); !ok {
				e.ErrorString("base fix \"" + strs[1] + "\" unknown")
			} else if hdg, err := strconv.Atoi(strs[2]); err != nil {
				e.ErrorString("heading \"%s\": %v", strs[2], err)
			} else if dist, err := strconv.ParseFloat(strs[3], 32); err != nil {
				e.ErrorString("distance \"%s\": %v", strs[3], err)
			} else {
				// Offset along the given heading and distance from the fix.
				p := math.LL2NM(pll, sg.NmPerLongitude)
				h := math.Radians(float32(hdg))
				v := [2]float32{math.Sin(h), math.Cos(h)}
				v = math.Scale2f(v, float32(dist))
				p = math.Add2f(p, v)
				sg.Fixes[fix] = math.NM2LL(p, sg.NmPerLongitude)
			}
		} else if pos, ok := sg.Locate(location); ok {
			// It's something simple. Check this after FIX@HDG/DIST,
			// though, since the runway matching KJFK-31L discards stuff
			// after the runway and we don't want that to match in that
			// case.
			sg.Fixes[fix] = pos
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

	if sg.PrimaryAirport == "" {
		e.ErrorString("\"primary_airport\" not specified")
	} else if ap, ok := av.DB.Airports[sg.PrimaryAirport]; !ok {
		e.ErrorString("\"primary_airport\" \"%s\" unknown", sg.PrimaryAirport)
	} else if mvar, err := av.DB.MagneticGrid.Lookup(ap.Location); err != nil {
		e.ErrorString("%s: unable to find magnetic declination: %v", sg.PrimaryAirport, err)
	} else {
		sg.MagneticVariation = mvar + sg.MagneticAdjustment
	}

	if len(sg.Airports) == 0 {
		e.ErrorString("No \"airports\" specified in scenario group")
	}
	for name, ap := range sg.Airports {
		e.Push("Airport " + name)
		ap.PostDeserialize(name, sg, sg.NmPerLongitude, sg.MagneticVariation,
			sg.ControlPositions, sg.STARSFacilityAdaptation.Scratchpads, sg.Airports, e)
		e.Pop()
	}

	fa := sg.STARSFacilityAdaptation
	if len(fa.VideoMapNames) == 0 {
		if len(fa.ControllerConfigs) == 0 {
			e.ErrorString("must provide one of \"stars_maps\" or \"controller_configs\" in \"stars_config\"")
			fa.ControllerConfigs = util.CommaKeyExpand(fa.ControllerConfigs)
		}
	} else if len(fa.ControllerConfigs) > 0 {
		e.ErrorString("cannot provide both \"stars_maps\" and \"controller_configs\" in \"stars_config\"")
	}

	if _, ok := sg.Scenarios[sg.DefaultScenario]; !ok {
		e.ErrorString("default scenario \"%s\" not found in \"scenarios\"", sg.DefaultScenario)
	}

	for _, aa := range sg.STARSFacilityAdaptation.AirspaceAwareness {
		e.Push("stars_adaptation")

		for _, fix := range aa.Fix {
			if _, ok := sg.Locate(fix); !ok && fix != "ALL" {
				e.ErrorString(fix + ": fix unknown")
			}
		}

		if aa.AltitudeRange[0] > aa.AltitudeRange[1] {
			e.ErrorString("lower end of \"altitude_range\" %d above upper end %d",
				aa.AltitudeRange[0], aa.AltitudeRange[1])
		}

		if _, ok := sg.ControlPositions[aa.ReceivingController]; !ok {
			e.ErrorString(aa.ReceivingController + ": controller unknown")
		}

		for _, t := range aa.AircraftType {
			if t != "J" && t != "T" && t != "P" {
				e.ErrorString("\"%s\": invalid \"aircraft_type\". Expected \"J\", \"T\", or \"P\".", t)
			}
		}

		e.Pop()
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

	for name, flow := range sg.InboundFlows {
		e.Push("Inbound flow " + name)
		if len(flow.Arrivals) == 0 && len(flow.Overflights) == 0 {
			e.ErrorString("no arrivals or overflights in inbound flow group")
		}

		for i := range flow.Arrivals {
			flow.Arrivals[i].PostDeserialize(sg, sg.NmPerLongitude, sg.MagneticVariation,
				sg.Airports, sg.ControlPositions, e)
		}
		for i := range flow.Overflights {
			flow.Overflights[i].PostDeserialize(sg, sg.NmPerLongitude, sg.MagneticVariation,
				sg.Airports, sg.ControlPositions, e)
		}

		e.Pop()
	}

	for _, rp := range sg.ReportingPointStrings {
		if loc, ok := sg.Locate(rp); !ok {
			e.ErrorString("unknown \"reporting_point\" \"%s\"", rp)
		} else {
			sg.ReportingPoints = append(sg.ReportingPoints, av.ReportingPoint{Fix: rp, Location: loc})
		}
	}

	// Do after airports!
	if len(sg.Scenarios) == 0 {
		e.ErrorString("No \"scenarios\" specified")
	}
	for name, s := range sg.Scenarios {
		e.Push("Scenario " + name)
		s.PostDeserialize(sg, e)
		e.Pop()
	}

	initializeSimConfigurations(sg, simConfigurations, multiController, e)
}

func (s *STARSFacilityAdaptation) PostDeserialize(e *util.ErrorLogger, sg *ScenarioGroup) {
	e.Push("stars_config")

	// Video maps
	for m := range s.VideoMapLabels {
		if !slices.Contains(s.VideoMapNames, m) {
			e.ErrorString("video map \"%s\" in \"map_labels\" is not in \"stars_maps\"", m)
		}
	}
	if len(s.VideoMapNames) > 0 {
		// Don't try to validate the map names here since we haven't loaded
		// the video maps yet. (Chicken and egg: we use the map names when
		// loading maps to figure out which ones we need rendering command
		// buffers for, so hence we haven't loaded them at this point.)
	} else if len(s.ControllerConfigs) > 0 {
		s.ControllerConfigs = util.CommaKeyExpand(s.ControllerConfigs)

		for ctrl, config := range s.ControllerConfigs {
			if pos, ok := sg.Locate(config.CenterString); !ok {
				e.ErrorString("unknown location \"%s\" specified for \"center\"", s.CenterString)
			} else {
				config.Center = pos
				s.ControllerConfigs[ctrl] = config
			}
		}

		for ctrl, config := range s.ControllerConfigs {
			if len(config.VideoMapNames) == 0 {
				e.ErrorString("must provide \"video_maps\" for controller \"%s\"", ctrl)
			}

			for _, name := range config.DefaultMaps {
				if !slices.Contains(config.VideoMapNames, name) {
					e.ErrorString("default map \"%s\" for \"%s\" is not included in the controller's "+
						"\"controller_maps\"", name, ctrl)
				}
			}
			// Make sure all of the control positions are included in at least
			// one of the scenarios.  As with VideoMapNames, don't try to
			// validate the map names yet.
			if !func() bool {
				for _, sc := range sg.Scenarios {
					if ctrl == sc.SoloController {
						return true
					}
					for _, config := range sc.SplitConfigurations {
						for pos := range config {
							if ctrl == pos {
								return true
							}
						}
					}
				}
				return false
			}() {
				e.ErrorString("Control position \"%s\" in \"controller_configs\" not found in any of the scenarios", ctrl)
			}
		}
	} else {
		e.ErrorString("Must specify either \"controller_configs\" or \"stars_maps\"")
	}

	if s.CenterString == "" {
		e.ErrorString("No \"center\" specified")
	} else if pos, ok := sg.Locate(s.CenterString); !ok {
		e.ErrorString("unknown location \"%s\" specified for \"center\"", s.CenterString)
	} else {
		s.Center = pos
	}

	if s.Range == 0 {
		s.Range = 50
	}

	for name, rs := range s.RadarSites {
		e.Push("Radar site " + name)
		if p, ok := sg.Locate(rs.PositionString); rs.PositionString == "" || !ok {
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

	for fix, fixes := range s.CoordinationFixes {
		e.Push("Coordination fix " + fix)
		// FIXME(mtrokel)
		/*
			if _, ok := sg.Locate(fix); !ok {
				e.ErrorString("coordination fix \"%v\" cannot be located", fix)
			}
		*/
		acceptableTypes := []string{"route", "zone"}
		for i, fix := range fixes {
			e.Push(fmt.Sprintf("Number %v", i))
			if !slices.Contains(acceptableTypes, fix.Type) {
				e.ErrorString("type \"%v\" is invalid. Valid types are \"route\" and \"zone\"", fix.Type)
			}
			if fix.Altitude[0] < 0 {
				e.ErrorString("bottom altitude \"%v\" is below zero", fix.Altitude[0])
			}
			if fix.Altitude[0] > fix.Altitude[1] {
				e.ErrorString("bottom altitude \"%v\" is higher than the top altitude \"%v\"", fix.Altitude[0], fix.Altitude[1])
			}
			if _, ok := av.DB.TRACONs[fix.ToFacility]; !ok {
				if _, ok := av.DB.ARTCCs[fix.ToFacility]; !ok {
					e.ErrorString("to facility \"%v\" is invalid", fix.ToFacility)
				}
			}
			if _, ok := av.DB.TRACONs[fix.FromFacility]; !ok {
				if _, ok := av.DB.ARTCCs[fix.FromFacility]; !ok {
					e.ErrorString("from facility \"%v\" is invalid", fix.FromFacility)
				}
			}
			e.Pop()
		}
		e.Pop()
	}

	for char, airport := range s.SingleCharAIDs {
		e.Push("Airport ID " + char)
		if _, ok := sg.Airports[airport]; !ok {
			e.ErrorString("airport\"%v\" isn't specified", airport)
		}
		e.Pop()
	}
	// if s.BeaconBank > 7 || s.BeaconBank < 1 {
	// 	e.ErrorString("beacon bank \"%v\" is invalid. Must be between 1 and 7", s.BeaconBank)
	// }

	if s.PDB.SplitGSAndCWT && s.PDB.ShowAircraftType {
		e.ErrorString("Both \"split_gs_and_cwt\" and \"show_aircraft_type\" cannot be specified for \"pdb\" adaption.")
	}
	if s.PDB.SplitGSAndCWT && s.PDB.HideGroundspeed {
		e.ErrorString("Both \"split_gs_and_cwt\" and \"hide_gs\" cannot be specified for \"pdb\" adaption.")
	}

	e.Pop() // stars_config
}

func (fa *STARSFacilityAdaptation) GetCoordinationFix(fp *STARSFlightPlan, acpos math.Point2LL, waypoints []av.Waypoint) (string, bool) {
	for fix, adaptationFixes := range fa.CoordinationFixes {
		if adaptationFix, err := adaptationFixes.Fix(fp.Altitude); err == nil {
			if adaptationFix.Type == av.ZoneBasedFix {
				// Exclude zone based fixes for now. They come in after the route-based fix
				continue
			}

			// FIXME (as elsewhere): make this more robust
			if strings.Contains(fp.Route, fix) {
				return fix, true
			}

			// FIXME: why both this and checking fp.Route?
			for _, waypoint := range waypoints {
				if waypoint.Fix == fix {
					return fix, true
				}
			}
		}

	}

	var closestFix string
	minDist := float32(1e30)
	for fix, adaptationFixes := range fa.CoordinationFixes {
		for _, adaptationFix := range adaptationFixes {
			if adaptationFix.Type == av.ZoneBasedFix {
				if loc, ok := av.DB.LookupWaypoint(fix); !ok {
					// FIXME: check this (if it isn't already) at scenario load time.
					panic(fix + ": not found in fixes database")
				} else if dist := math.NMDistance2LL(acpos, loc); dist < minDist {
					minDist = dist
					closestFix = fix
				}
			}
		}
	}

	return closestFix, closestFix != ""
}

func initializeSimConfigurations(sg *ScenarioGroup,
	simConfigurations map[string]map[string]*Configuration, multiController bool, e *util.ErrorLogger) {
	config := &Configuration{
		ScenarioConfigs:  make(map[string]*SimScenarioConfiguration),
		ControlPositions: sg.ControlPositions,
		DefaultScenario:  sg.DefaultScenario,
	}

	for name, scenario := range sg.Scenarios {
		sc := &SimScenarioConfiguration{
			SplitConfigurations: scenario.SplitConfigurations,
			LaunchConfig:        MakeLaunchConfig(scenario.DepartureRunways, scenario.InboundFlowDefaultRates),
			Wind:                scenario.Wind,
			DepartureRunways:    scenario.DepartureRunways,
			ArrivalRunways:      scenario.ArrivalRunways,
			PrimaryAirport:      sg.PrimaryAirport,
		}

		if multiController {
			if len(scenario.SplitConfigurations) == 0 {
				// not a multi-controller scenario
				continue
			}
			var err error
			sc.SelectedController, err = scenario.SplitConfigurations.GetPrimaryController(scenario.DefaultSplit)
			if err != nil {
				e.Error(err)
			}
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
		// The default scenario may be invalid; e.g. if it's single
		// controller but we're gathering multi-controller here. Pick
		// something valid in that case.
		if _, ok := config.ScenarioConfigs[config.DefaultScenario]; !ok {
			config.DefaultScenario = util.SortedMapKeys(config.ScenarioConfigs)[0]
		}

		if simConfigurations[sg.TRACON] == nil {
			simConfigurations[sg.TRACON] = make(map[string]*Configuration)
		}
		simConfigurations[sg.TRACON][sg.Name] = config
	}
}

///////////////////////////////////////////////////////////////////////////
// Airspace

func InAirspace(p math.Point2LL, alt float32, volumes []ControllerAirspaceVolume) (bool, [][2]int) {
	var altRanges [][2]int
	for _, v := range volumes {
		inside := false
		for _, pts := range v.Boundaries {
			if math.PointInPolygon2LL(p, pts) {
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

func loadScenarioGroup(filesystem fs.FS, path string, e *util.ErrorLogger) *ScenarioGroup {
	e.Push("File " + path)
	defer e.Pop()

	contents, err := fs.ReadFile(filesystem, path)
	if err != nil {
		e.Error(err)
		return nil
	}

	util.CheckJSONVsSchema[ScenarioGroup](contents, e)
	if e.HaveErrors() {
		return nil
	}

	var s ScenarioGroup
	if err := util.UnmarshalJSON(contents, &s); err != nil {
		e.Error(err)
		return nil
	}
	if s.Name == "" {
		e.ErrorString("scenario group is missing \"name\"")
		return nil
	}
	if s.TRACON == "" {
		e.ErrorString("scenario group is missing \"tracon\"")
		return nil
	}
	return &s
}

type RootFS struct{}

func (r RootFS) Open(filename string) (fs.File, error) {
	return os.Open(filename)
}

type dbResolver struct{}

func (d *dbResolver) Resolve(s string) (math.Point2LL, error) {
	if n, ok := av.DB.Navaids[s]; ok {
		return n.Location, nil
	} else if n, ok := av.DB.Airports[s]; ok {
		return n.Location, nil
	} else if f, ok := av.DB.Fixes[s]; ok {
		return f.Location, nil
	} else {
		return math.Point2LL{}, fmt.Errorf("%s: unknown fix", s)
	}
}

// LoadScenarioGroups loads all of the available scenarios, both from the
// scenarios/ directory in the source code distribution as well as,
// optionally, a scenario file provided on the command line.  It doesn't
// try to do any sort of meaningful error handling but it does try to
// continue on in the presence of errors; all errors will be printed and
// the program will exit if there are any.  We'd rather force any errors
// due to invalid scenario definitions to be fixed...
func LoadScenarioGroups(isLocal bool, extraScenarioFilename string, extraVideoMapFilename string,
	e *util.ErrorLogger, lg *log.Logger) (map[string]map[string]*ScenarioGroup, map[string]map[string]*Configuration, *av.VideoMapLibrary) {
	start := time.Now()

	math.SetLocationResolver(&dbResolver{})

	// First load the scenarios.
	scenarioGroups := make(map[string]map[string]*ScenarioGroup)
	simConfigurations := make(map[string]map[string]*Configuration)
	referencedVideoMaps := make(map[string]map[string]interface{}) // filename -> map name -> used
	updateReferencedMaps := func(fa STARSFacilityAdaptation) {
		if referencedVideoMaps[fa.VideoMapFile] == nil {
			referencedVideoMaps[fa.VideoMapFile] = make(map[string]interface{})
		}
		for _, m := range fa.VideoMapNames {
			referencedVideoMaps[fa.VideoMapFile][m] = nil
		}
		for _, config := range fa.ControllerConfigs {
			for _, m := range config.VideoMapNames {
				referencedVideoMaps[fa.VideoMapFile][m] = nil
			}
		}
	}

	err := util.WalkResources("scenarios", func(path string, d fs.DirEntry, fs fs.FS, err error) error {
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

		// This is a terrible hack, but the Windows WIX installer toolkit
		// is even more so, so here we go. For reasons not understood, it's
		// not removing the old bdl.json file on an upgrade install; since
		// it got renamed to y90.json, having both gives errors about
		// scenarios being redefined. So cull it here instead...
		if strings.ToLower(filepath.Base(path)) == "bdl.json" {
			return nil
		}

		lg.Infof("%s: loading scenario", path)
		s := loadScenarioGroup(fs, path, e)
		if s != nil {
			if _, ok := scenarioGroups[s.TRACON][s.Name]; ok {
				e.ErrorString("%s / %s: scenario redefined", s.TRACON, s.Name)
			} else {
				if scenarioGroups[s.TRACON] == nil {
					scenarioGroups[s.TRACON] = make(map[string]*ScenarioGroup)
				}
				scenarioGroups[s.TRACON][s.Name] = s
			}
			updateReferencedMaps(s.STARSFacilityAdaptation)
		}
		return nil
	})
	if err != nil {
		e.Error(err)
	}
	if e.HaveErrors() {
		// Don't keep going since we'll likely crash in the following
		return nil, nil, nil
	}

	// Load the scenario specified on command line, if any.
	if extraScenarioFilename != "" {
		fs := func() fs.FS {
			if filepath.IsAbs(extraScenarioFilename) {
				return RootFS{}
			} else {
				return os.DirFS(".")
			}
		}()
		s := loadScenarioGroup(fs, extraScenarioFilename, e)
		if s != nil {
			// These are allowed to redefine an existing scenario.
			if scenarioGroups[s.TRACON] == nil {
				scenarioGroups[s.TRACON] = make(map[string]*ScenarioGroup)
			}
			scenarioGroups[s.TRACON][s.Name] = s

			// These may have an empty "video_map_file" member, which
			// is automatically patched up here...
			if s.STARSFacilityAdaptation.VideoMapFile == "" {
				if extraVideoMapFilename != "" {
					s.STARSFacilityAdaptation.VideoMapFile = extraVideoMapFilename
				} else {
					e.ErrorString("%s: no \"video_map_file\" in scenario and -videomap not specified",
						extraScenarioFilename)
				}
			}
			updateReferencedMaps(s.STARSFacilityAdaptation)
		}
	}

	// Next load the video maps; we will kick off work to load
	maplib := av.MakeVideoMapLibrary()
	err = util.WalkResources("videomaps", func(path string, d fs.DirEntry, fs fs.FS, err error) error {
		if err != nil {
			lg.Errorf("error walking videomaps: %v", err)
			return nil
		}

		if d.IsDir() {
			return nil
		}

		if strings.HasSuffix(path, "-videomaps.gob") || strings.HasSuffix(path, "-videomaps.gob.zst") {
			maplib.AddFile(fs, path, referencedVideoMaps[path], e)
		}

		return nil
	})
	if err != nil {
		lg.Errorf("error loading videomaps: %v", err)
		os.Exit(1)
	}

	lg.Infof("scenario/video map manifest load time: %s\n", time.Since(start))

	// Load the video map specified on the command line, if any.
	if extraVideoMapFilename != "" {
		fs := func() fs.FS {
			if filepath.IsAbs(extraVideoMapFilename) {
				return RootFS{}
			} else {
				return os.DirFS(".")
			}
		}()
		maplib.AddFile(fs, extraVideoMapFilename, referencedVideoMaps[extraVideoMapFilename], e)
	}

	// Final tidying before we return the loaded scenarios.
	for tname, tracon := range scenarioGroups {
		e.Push("TRACON " + tname)

		scenarioNames := make(map[string]string)

		for groupName, sgroup := range tracon {
			e.Push("Scenario group " + groupName)

			// Make sure the same scenario name isn't used in multiple
			// group definitions.
			for scenarioName := range sgroup.Scenarios {
				if other, ok := scenarioNames[scenarioName]; ok {
					e.ErrorString("scenario \"%s\" is also defined in the \"%s\" scenario group",
						scenarioName, other)
				}
				scenarioNames[scenarioName] = groupName
			}

			// Make sure we have what we need in terms of video maps
			fa := &sgroup.STARSFacilityAdaptation
			if vf := fa.VideoMapFile; vf == "" {
				e.ErrorString("no \"video_map_file\" specified")
			} else if !maplib.HaveFile(vf) {
				e.ErrorString("no manifest for video map \"%s\" found. Options: %s", vf,
					strings.Join(maplib.AvailableFiles(), ", "))
			} else {
				for _, name := range fa.VideoMapNames {
					if name != "" && !maplib.HaveMap(vf, name) {
						e.ErrorString("video map \"%s\" not found. Use -listmaps <path to Zxx-videomaps.gob.zst> to show available video maps for an ARTCC.",
							name)
					}
				}
			}

			multiController := !isLocal
			sgroup.PostDeserialize(multiController, e, simConfigurations)

			e.Pop()
		}
		e.Pop()
	}

	// Walk all of the scenario groups to get all of the possible departing aircraft
	// types to see where V2 is needed in the performance database..
	acTypes := make(map[string]struct{})

	for _, tracon := range scenarioGroups {
		for _, sg := range tracon {
			for _, ap := range sg.Airports {
				for _, dep := range ap.Departures {
					for _, al := range dep.Airlines {
						fleet := util.Select(al.Fleet != "", al.Fleet, "default")
						for _, ac := range av.DB.Airlines[al.ICAO].Fleets[fleet] {
							acTypes[ac.ICAO] = struct{}{}
						}
					}
				}
			}
		}
	}
	var missing []string
	for _, t := range util.SortedMapKeys(acTypes) {
		if av.DB.AircraftPerformance[t].Speed.V2 == 0 {
			missing = append(missing, t)
		}
	}
	lg.Warnf("Missing V2 in performance database: %s", strings.Join(missing, ", "))

	return scenarioGroups, simConfigurations, maplib
}

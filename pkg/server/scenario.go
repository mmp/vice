// pkg/server/scenario.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
)

type ScenarioGroup struct {
	TRACON           string                     `json:"tracon"`
	Name             string                     `json:"name"`
	Airports         map[string]*av.Airport     `json:"airports"`
	Fixes            map[string]math.Point2LL   `json:"-"`
	FixesStrings     util.OrderedMap            `json:"fixes"`
	Scenarios        map[string]*Scenario       `json:"scenarios"`
	DefaultScenario  string                     `json:"default_scenario"`
	ControlPositions map[string]*av.Controller  `json:"control_positions"`
	Airspace         av.Airspace                `json:"airspace"`
	InboundFlows     map[string]*av.InboundFlow `json:"inbound_flows"`

	PrimaryAirport string `json:"primary_airport"`

	ReportingPointStrings []string            `json:"reporting_points"`
	ReportingPoints       []av.ReportingPoint // not in JSON

	NmPerLatitude           float32 // Always 60
	NmPerLongitude          float32 // Derived from Center
	MagneticVariation       float32
	MagneticAdjustment      float32                     `json:"magnetic_adjustment"`
	STARSFacilityAdaptation sim.STARSFacilityAdaptation `json:"stars_config"`
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

	Airspace map[string][]string `json:"airspace"`

	DepartureRunways []sim.DepartureRunway `json:"departure_runways,omitempty"`
	ArrivalRunways   []sim.ArrivalRunway   `json:"arrival_runways,omitempty"`

	Center       math.Point2LL `json:"-"`
	CenterString string        `json:"center"`
	Range        float32       `json:"range"`
	DefaultMaps  []string      `json:"default_maps"`
	VFRRateScale *float32      `json:"vfr_rate_scale"`
}

func (s *Scenario) PostDeserialize(sg *ScenarioGroup, e *util.ErrorLogger, manifest *sim.VideoMapManifest) {
	defer e.CheckDepth(e.CurrentDepth())

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

	for ctrl, vnames := range s.Airspace {
		e.Push("airspace")

		found := ctrl == s.SoloController
		// Check multi-controller
		for _, config := range s.SplitConfigurations {
			if _, ok := config[ctrl]; ok {
				found = true
			}
		}
		if !found {
			e.ErrorString("Controller %q not used in in scenario", ctrl)
		}
		for _, vname := range vnames {
			if _, ok := sg.Airspace.Volumes[vname]; !ok {
				e.ErrorString("Airspace volume %q for controller %q not defined in scenario group \"airspace\"",
					vname, ctrl)
			}
		}
		e.Pop()
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

	// Add controllers to virtual controllers if not present
	addController := func(tcp string) {
		if tcp != "" && !slices.Contains(s.VirtualControllers, tcp) {
			s.VirtualControllers = append(s.VirtualControllers, tcp)
		}
	}
	addControllersFromWaypoints := func(route []av.Waypoint) {
		for _, wp := range route {
			addController(wp.TCPHandoff)
		}
	}
	// Make sure all of the controllers used in airspace awareness will be there.
	for _, aa := range sg.STARSFacilityAdaptation.AirspaceAwareness {
		addController(aa.ReceivingController)
	}

	airportExits := make(map[string]map[string]interface{}) // airport -> exit -> is it covered
	for i, rwy := range s.DepartureRunways {
		e.Push("Departure runway " + rwy.Airport + " " + rwy.Runway)

		if airportExits[rwy.Airport] == nil {
			airportExits[rwy.Airport] = make(map[string]interface{})
		}

		if ap, ok := sg.Airports[rwy.Airport]; !ok {
			e.ErrorString("airport not found in scenario group \"airports\"")
		} else {
			if routes, ok := ap.DepartureRoutes[rwy.Runway]; !ok {
				e.ErrorString("runway departure routes not found")
			} else {
				s.DepartureRunways[i].ExitRoutes = routes
				for exit := range routes {
					// It's fine if multiple active runways cover the exit.
					airportExits[rwy.Airport][exit] = nil
				}

				for _, r := range routes {
					addControllersFromWaypoints(r.Waypoints)
				}
			}

			if len(ap.Departures) == 0 {
				e.ErrorString("no \"departures\" specified for airport")
			}

			if rwy.Category != "" {
				found := slices.ContainsFunc(ap.Departures, func(dep av.Departure) bool {
					return ap.ExitCategories[dep.Exit] == rwy.Category
				})
				if !found {
					e.ErrorString("no departures have exit category %q", rwy.Category)
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

	activeAirports := make(map[*av.Airport]interface{}) // all airports with departures or arrivals
	for _, rwy := range s.ArrivalRunways {
		e.Push("Arrival runway " + rwy.Airport + " " + rwy.Runway)

		if ap, ok := sg.Airports[rwy.Airport]; !ok {
			e.ErrorString("airport not found in scenario group \"airports\"")
		} else {
			activeAirports[ap] = nil

			found := false
			for _, appr := range ap.Approaches {
				if appr.Runway == rwy.Runway {
					found = true
				}
			}

			if !found {
				e.ErrorString("no approach found that reaches this runway")
			}
		}
		e.Pop()
	}

	if _, ok := sg.ControlPositions[s.SoloController]; s.SoloController != "" && !ok {
		e.ErrorString("controller %q for \"solo_controller\" is unknown", s.SoloController)
	}

	// Figure out which airports/runways and airports/SIDs are used in the scenario.
	activeAirportSIDs := make(map[string]map[string]interface{})
	activeAirportRunways := make(map[string]map[string]interface{})
	activeDepartureAirports := make(map[string]interface{})
	for _, rwy := range s.DepartureRunways {
		e.Push("departure runway " + rwy.Runway)

		ap, ok := sg.Airports[rwy.Airport]
		if !ok {
			e.ErrorString("%s: airport not found in \"airports\"", rwy.Airport)
		} else {
			activeAirports[ap] = nil
			activeDepartureAirports[rwy.Airport] = nil

			if ap.DepartureController != "" {
				// Make sure it's in the control positions
				if _, ok := sg.ControlPositions[ap.DepartureController]; !ok {
					e.ErrorString("controller %q for \"default_controller\" is unknown", ap.DepartureController)
				} else if !slices.Contains(s.VirtualControllers, ap.DepartureController) {
					s.VirtualControllers = append(s.VirtualControllers, ap.DepartureController)
				}
			} else {
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
		if ctrl, ok := sg.ControlPositions[s.SoloController]; ok {
			if ctrl.DefaultAirport == "" {
				e.ErrorString("%s: controller must have \"default_airport\" specified (required for CRDA).", ctrl.Position)
			}
		}
		for _, callsign := range s.SplitConfigurations.Splits() {
			if ctrl, ok := sg.ControlPositions[callsign]; ok && ctrl.DefaultAirport == "" {
				e.ErrorString("%s: controller must have \"default_airport\" specified (required for CRDA).", ctrl.Position)
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
			e.ErrorString("did not find \"default_split\" %q in \"multi_controllers\" splits", s.DefaultSplit)
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
				e.ErrorString("controller %q not defined in the scenario group's \"control_positions\"", callsign)
			}

			// Make sure any airports claimed for departures are valid
			for _, airportSID := range ctrl.Departures {
				ap, sidRunway, haveSIDRunway := strings.Cut(airportSID, "/")
				if sids, ok := activeAirportSIDs[ap]; !ok {
					e.ErrorString("airport %q is not departing aircraft in this scenario", ap)
				} else if haveSIDRunway {
					// If there's something after a slash, make sure it's
					// either a valid SID or runway.
					_, okSID := sids[sidRunway]
					_, okRunway := activeAirportRunways[ap][sidRunway]
					if !okSID && !okRunway {
						e.ErrorString("%q at airport %q is neither an active runway or SID in this scenario", sidRunway, ap)
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
					e.ErrorString("inbound flow %q not found in scenario \"inbound_rates\"", flow)
				} else if f, ok := sg.InboundFlows[flow]; !ok {
					e.ErrorString("inbound flow %q not found in scenario group \"inbound_flows\"", flow)
				} else {
					// Is there a handoff to a human controller?
					overflightHasHandoff := func(of av.Overflight) bool {
						return slices.ContainsFunc(of.Waypoints, func(wp av.Waypoint) bool { return wp.HumanHandoff })
					}
					if len(f.Arrivals) == 0 && !slices.ContainsFunc(f.Overflights, overflightHasHandoff) {
						// It's just overflights without handoffs
						e.ErrorString("no inbound flows in %q have handoffs", flow)
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
				e.ErrorString("Backup controller %q for %q is unknown",
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
				e.ErrorString("controller %q doesn't have a valid backup controller", callsign)
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
			for _, ar := range flow.Arrivals {
				addController(ar.InitialController)
				addControllersFromWaypoints(ar.Waypoints)
			}
			for _, of := range flow.Overflights {
				addController(of.InitialController)
				addControllersFromWaypoints(of.Waypoints)
			}

			// Check the airports in it
			for category := range s.InboundFlowDefaultRates[name] {
				if category == "overflights" {
					if len(flow.Overflights) == 0 {
						e.ErrorString("Rate specified for \"overflights\" but no overflights specified in %q", name)
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
									func(r sim.ArrivalRunway) bool {
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
			hasHandoff := false
			for _, ar := range flow.Arrivals {
				if slices.ContainsFunc(ar.Waypoints, func(wp av.Waypoint) bool { return wp.HumanHandoff }) {
					hasHandoff = true
				}
			}
			for _, of := range flow.Overflights {
				if slices.ContainsFunc(of.Waypoints, func(wp av.Waypoint) bool { return wp.HumanHandoff }) {
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
						e.ErrorString("no controller in \"multi_controllers\" has %q in their \"inbound_flows\"", name)
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
			e.ErrorString("controller %q unknown", ctrl)
		}
	}

	if s.CenterString != "" {
		if pos, ok := sg.Locate(s.CenterString); !ok {
			e.ErrorString("unknown location %q specified for \"center\"", s.CenterString)
		} else {
			s.Center = pos
		}
	}

	// STARSFacilityAdaptaion checks
	fa := &sg.STARSFacilityAdaptation
	if len(s.DefaultMaps) > 0 {
		if len(fa.ControllerConfigs) > 0 {
			e.ErrorString("\"default_maps\" is not allowed when \"controller_configs\" is set in \"stars_config\"")
		}

		for _, dm := range s.DefaultMaps {
			if !manifest.HasMap(dm) {
				e.ErrorString("video map %q in \"default_maps\" not found. Use -listmaps "+
					"<path to Zxx-videomaps.gob.zst> to show available video maps for an ARTCC.", dm)
			}
		}
	} else if len(fa.ControllerConfigs) > 0 {
		// Make sure that each controller in the scenario is represented in
		// "controller_configs".
		if _, ok := fa.ControllerConfigs[s.SoloController]; !ok {
			e.ErrorString("control position %q is not included in \"controller_configs\"",
				s.SoloController)
		}
		for _, conf := range s.SplitConfigurations {
			for pos := range conf {
				if _, ok := fa.ControllerConfigs[pos]; !ok {
					e.ErrorString("control position %q is not included in \"controller_configs\"", pos)
				}
			}
		}

		// Handle beacon code blocks
		for _, config := range fa.ControllerConfigs {
			config.MonitoredBeaconCodeBlocks = nil // HACK: this is aliased if multiple controllers share a config.
			if config.MonitoredBeaconCodeBlocksString == nil {
				// None specified: 12xx block by default
				config.MonitoredBeaconCodeBlocks = append(config.MonitoredBeaconCodeBlocks, 0o12)
			} else {
				for _, s := range strings.Split(*config.MonitoredBeaconCodeBlocksString, ",") {
					s = strings.TrimSpace(s)
					if code, err := av.ParseSquawkOrBlock(s); err != nil {
						e.ErrorString("invalid beacon code %q in \"beacon_code_blocks\": %v", s, err)
					} else {
						config.MonitoredBeaconCodeBlocks = append(config.MonitoredBeaconCodeBlocks, code)
					}
				}
			}
		}
	}

	e.Push("\"flight_plan\"")
	fa.FlightPlan.QuickACID = strings.ToUpper(fa.FlightPlan.QuickACID)
	if qa := fa.FlightPlan.QuickACID; qa == "" {
		fa.FlightPlan.QuickACID = "VCE"
	} else {
		if qa[0] < 'A' || qa[0] > 'Z' {
			e.ErrorString("\"quick_acid\" must start with a letter")
		}
		if len(qa) > 3 {
			e.ErrorString("\"quick_acid\" can't be more than three characters")
		}
	}
	for abbrev, exp := range fa.FlightPlan.ACIDExpansions {
		if len(abbrev) != 1 {
			e.ErrorString("Abbreviation %q is not allowed: must be a single character", abbrev)
		}
		if !strings.Contains("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789+.*^/", abbrev) {
			e.ErrorString("Abbreviation %q must be A-Z, 0-9, +, ., *, ^, or /", abbrev)
		}
		if len(exp) == 0 {
			e.ErrorString("Must specify an expansion for %q", abbrev)
		} else if exp[0] < 'A' || exp[0] > 'Z' {
			e.ErrorString("Expansion %q for %q must start with a letter", exp, abbrev)
		}
	}
	e.Pop()

	if s.VFRRateScale == nil { // unspecified -> default to 1
		one := float32(1)
		s.VFRRateScale = &one
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

func (sg *ScenarioGroup) Similar(fix string) []string {
	d1, d2 := util.SelectInTwoEdits(fix, maps.Keys(sg.Fixes), nil, nil)
	d1, d2 = util.SelectInTwoEdits(fix, maps.Keys(av.DB.Navaids), d1, d2)
	d1, d2 = util.SelectInTwoEdits(fix, maps.Keys(av.DB.Airports), d1, d2)
	d1, d2 = util.SelectInTwoEdits(fix, maps.Keys(av.DB.Fixes), d1, d2)
	return util.Select(len(d1) > 0, d1, d2)
}

var (
	// "FIX@HDG/DIST"
	reFixHeadingDistance = regexp.MustCompile(`^([\w-]{3,})@([\d]{3})/(\d+(\.\d+)?)$`)
)

func (sg *ScenarioGroup) PostDeserialize(multiController bool, e *util.ErrorLogger, simConfigurations map[string]map[string]*Configuration,
	manifest *sim.VideoMapManifest) {
	defer e.CheckDepth(e.CurrentDepth())

	// Rewrite legacy files to be TCP-based.
	sg.rewriteControllers(e)

	// stars_config items. This goes first because we need to initialize
	// Center (and thence NmPerLongitude) ASAP.
	if ctr := sg.STARSFacilityAdaptation.CenterString; ctr == "" {
		e.ErrorString("No \"center\" specified")
	} else if pos, ok := sg.Locate(ctr); !ok {
		e.ErrorString("unknown location %q specified for \"center\"", ctr)
	} else {
		sg.STARSFacilityAdaptation.Center = pos
	}

	sg.NmPerLatitude = 60
	sg.NmPerLongitude = 60 * math.Cos(math.Radians(sg.STARSFacilityAdaptation.Center[1]))

	if sg.TRACON == "" {
		e.ErrorString("\"tracon\" must be specified")
	} else if _, ok := av.DB.TRACONs[sg.TRACON]; !ok {
		e.ErrorString("TRACON %q is unknown; it must be a 3-letter identifier listed at "+
			"https://www.faa.gov/about/office_org/headquarters_offices/ato/service_units/air_traffic_services/tracon.",
			sg.TRACON)
	}

	sg.Fixes = make(map[string]math.Point2LL)
	for _, fix := range sg.FixesStrings.Keys() {
		loc, _ := sg.FixesStrings.Get(fix)
		location, ok := loc.(string)
		if !ok {
			e.ErrorString("location for fix %q is not a string: %+v", fix, loc)
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
				e.ErrorString("heading %q: %v", strs[2], err)
			} else if dist, err := strconv.ParseFloat(strs[3], 32); err != nil {
				e.ErrorString("distance %q: %v", strs[3], err)
			} else {
				// Offset along the given heading and distance from the fix.
				sg.Fixes[fix] = math.Offset2LL(pll, float32(hdg), float32(dist), sg.NmPerLongitude,
					sg.MagneticVariation)
			}
		} else if pos, ok := sg.Locate(location); ok {
			// It's something simple. Check this after FIX@HDG/DIST,
			// though, since the runway matching KJFK-31L discards stuff
			// after the runway and we don't want that to match in that
			// case.
			sg.Fixes[fix] = pos
		} else {
			e.ErrorString("invalid location syntax %q for fix %q", location, fix)
		}

		e.Pop()
	}

	PostDeserializeSTARSFacilityAdaptation(&sg.STARSFacilityAdaptation, e, sg, manifest)

	for name, volumes := range sg.Airspace.Volumes {
		for i, vol := range volumes {
			e.Push("Airspace volume " + name)

			for _, b := range vol.BoundaryNames {
				if pts, ok := sg.Airspace.Boundaries[b]; !ok {
					e.ErrorString("airspace boundary %q not found", b)
				} else {
					sg.Airspace.Volumes[name][i].Boundaries = append(sg.Airspace.Volumes[name][i].Boundaries, pts)
				}
			}

			if vol.Label == "" {
				// Default label if none specified
				if vol.LowerLimit == vol.UpperLimit {
					sg.Airspace.Volumes[name][i].Label = fmt.Sprintf("%d", vol.LowerLimit/100)
				} else {
					sg.Airspace.Volumes[name][i].Label = fmt.Sprintf("%d-%d", vol.LowerLimit/100, vol.UpperLimit/100)
				}
			}
			if vol.LabelPosition.IsZero() {
				// Label at the center if no center specified
				e := math.EmptyExtent2D()
				for _, pts := range sg.Airspace.Volumes[name][i].Boundaries {
					for _, p := range pts {
						e = math.Union(e, p)
					}
				}
				sg.Airspace.Volumes[name][i].LabelPosition = e.Center()
			}

			e.Pop()
		}
	}

	if sg.PrimaryAirport == "" {
		e.ErrorString("\"primary_airport\" not specified")
	} else if ap, ok := av.DB.Airports[sg.PrimaryAirport]; !ok {
		e.ErrorString("\"primary_airport\" %q unknown", sg.PrimaryAirport)
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

	if _, ok := sg.Scenarios[sg.DefaultScenario]; !ok {
		e.ErrorString("default scenario %q not found in \"scenarios\"", sg.DefaultScenario)
	}

	for position, ctrl := range sg.ControlPositions {
		e.Push("Controller " + position)

		if ctrl.Frequency < 118000 || ctrl.Frequency > 138000 {
			e.ErrorString("invalid frequency: %6.3f", float32(ctrl.Frequency)/1000)
		}
		if ctrl.TCP == "" {
			e.ErrorString("no \"sector_id\" specified")
		}
		if ctrl.RadioName == "" {
			e.ErrorString("no \"radio_name\" specified")
		}

		if !ctrl.ERAMFacility && strings.HasSuffix(strings.ToLower(ctrl.RadioName), "center") {
			e.ErrorString("missing \"eram_facility\" for center controller")
		}
		if ctrl.ERAMFacility {
			if ctrl.FacilityIdentifier == "" {
				e.ErrorString("must specify \"facility_id\" if \"eram_facility\" is set")
			}
			if len(ctrl.TCP) < 2 {
				e.ErrorString("must specify both facility and numeric sector for center controller")
			} else {
				if !(ctrl.TCP[0] >= 'A' && ctrl.TCP[0] <= 'Z') {
					e.ErrorString("first character of center controller \"sector_id\" must be a letter")
				}
				if _, err := strconv.Atoi(ctrl.TCP[1:]); err != nil {
					e.ErrorString("center controller \"sector_id\" must end with a number")
				}
			}
		}

		// Is an explicitly-given scope_char unnecessary?
		if ctrl.Scope != "" {
			if ctrl.FacilityIdentifier == ctrl.Scope {
				e.ErrorString("\"scope_char\" is redundant since it matches \"facility_id\"")
			}
			if !ctrl.ERAMFacility && ctrl.FacilityIdentifier == "" && len(ctrl.TCP) > 0 &&
				ctrl.Scope == string(ctrl.TCP[len(ctrl.TCP)-1]) {
				e.ErrorString("\"scope_char\" is redundant since it matches the last character of a local controller's \"sector_id\"")
			}
		}
		if len(ctrl.Scope) > 1 {
			e.ErrorString("\"scope_char\" may only be a single character")
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
			e.ErrorString("unknown \"reporting_point\" %q", rp)
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
		s.PostDeserialize(sg, e, manifest)
		e.Pop()
	}

	initializeSimConfigurations(sg, simConfigurations, multiController, e)
}

func (sg *ScenarioGroup) rewriteControllers(e *util.ErrorLogger) {
	// Grab the original keys before rewriting.
	for position, ctrl := range sg.ControlPositions {
		ctrl.Position = position
	}

	pos := make(map[string]*av.Controller)
	for _, ctrl := range sg.ControlPositions {
		id := ctrl.Id()
		if _, ok := pos[id]; ok {
			e.ErrorString("%s: TCP / sector_id used for multiple \"control_positions\"", id)
		}
		pos[id] = ctrl
	}

	rewrite := func(s *string) {
		if *s == "" {
			return
		}
		if ctrl, ok := sg.ControlPositions[*s]; ok {
			*s = ctrl.Id()
		}
	}
	rewriteWaypoints := func(wp av.WaypointArray) {
		for _, w := range wp {
			if w.TCPHandoff != "" {
				rewrite(&w.TCPHandoff)
			}
		}
	}

	for _, s := range sg.Scenarios {
		rewrite(&s.SoloController)

		if len(s.Airspace) > 0 {
			a := make(map[string][]string)
			for ctrl, vols := range s.Airspace {
				rewrite(&ctrl)
				a[ctrl] = vols
			}
			s.Airspace = a
		}

		for _, rwy := range s.DepartureRunways {
			if ap, ok := sg.Airports[rwy.Airport]; ok {
				rewrite(&ap.DepartureController)
			}
		}

		for name, config := range s.SplitConfigurations {
			for callsign, ctrl := range config {
				tcp := callsign
				rewrite(&tcp)
				rewrite(&ctrl.BackupController)

				delete(config, callsign)
				config[tcp] = ctrl
			}
			s.SplitConfigurations[name] = config
		}

		for i := range s.VirtualControllers {
			rewrite(&s.VirtualControllers[i])
		}
	}

	for _, ap := range sg.Airports {
		rewrite(&ap.DepartureController)

		for _, exitroutes := range ap.DepartureRoutes {
			for _, route := range exitroutes {
				rewrite(&route.HandoffController)
				rewriteWaypoints(route.Waypoints)
			}
		}

		for _, app := range ap.Approaches {
			for _, wps := range app.Waypoints {
				rewriteWaypoints(wps)
			}
		}
		for _, dep := range ap.Departures {
			rewriteWaypoints(dep.RouteWaypoints)
		}
	}

	fa := &sg.STARSFacilityAdaptation
	for i := range fa.AirspaceAwareness {
		rewrite(&fa.AirspaceAwareness[i].ReceivingController)
	}
	for cs, config := range fa.ControllerConfigs {
		// Rewrite comma separated list of callsigns
		tcps := strings.Split(cs, ",")
		for i, cs := range tcps {
			tcp := strings.TrimSpace(cs)
			rewrite(&tcp)
			tcps[i] = tcp
		}

		delete(fa.ControllerConfigs, cs)
		fa.ControllerConfigs[strings.Join(tcps, ",")] = config
	}

	for _, flow := range sg.InboundFlows {
		for i := range flow.Arrivals {
			rewrite(&flow.Arrivals[i].InitialController)
			rewriteWaypoints(flow.Arrivals[i].Waypoints)
		}
		for i := range flow.Overflights {
			rewrite(&flow.Overflights[i].InitialController)
			rewriteWaypoints(flow.Overflights[i].Waypoints)
		}
	}

	sg.ControlPositions = pos
}

func PostDeserializeSTARSFacilityAdaptation(s *sim.STARSFacilityAdaptation, e *util.ErrorLogger, sg *ScenarioGroup,
	manifest *sim.VideoMapManifest) {
	defer e.CheckDepth(e.CurrentDepth())

	e.Push("stars_config")

	// Video maps
	for m := range s.VideoMapLabels {
		if !slices.Contains(s.VideoMapNames, m) {
			e.ErrorString("video map %q in \"map_labels\" is not in \"stars_maps\"", m)
		}
	}
	for _, m := range s.VideoMapNames {
		if m != "" && !manifest.HasMap(m) {
			e.ErrorString("video map %q in \"stars_maps\" is not a valid video map", m)
		}
	}

	if len(s.ControllerConfigs) > 0 {
		var err error
		s.ControllerConfigs, err = util.CommaKeyExpand(s.ControllerConfigs)
		if err != nil {
			e.Error(err)
		}

		for ctrl, config := range s.ControllerConfigs {
			if config.CenterString != "" {
				if pos, ok := sg.Locate(config.CenterString); !ok {
					e.ErrorString("unknown location %q specified for \"center\"", s.CenterString)
				} else {
					config.Center = pos
					s.ControllerConfigs[ctrl] = config
				}
			}
		}

		for ctrl, config := range s.ControllerConfigs {
			if len(config.VideoMapNames) == 0 {
				e.ErrorString("must provide \"video_maps\" for controller %q", ctrl)
			}

			for _, name := range config.DefaultMaps {
				if !slices.Contains(config.VideoMapNames, name) {
					e.ErrorString("default map %q for %q is not included in the controller's "+
						"\"controller_maps\"", name, ctrl)
				}

				if !manifest.HasMap(name) {
					e.ErrorString("video map %q in \"default_maps\" for controller %q is not a valid video map",
						name, ctrl)
				}
			}
			for _, name := range config.VideoMapNames {
				if name != "" && !manifest.HasMap(name) {
					e.ErrorString("video map %q in \"video_maps\" for controller %q is not a valid video map",
						name, ctrl)
				}
			}

			// Make sure all of the control positions are included in at least
			// one of the scenarios.
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
				e.ErrorString("Control position %q in \"controller_configs\" not found in any of the scenarios", ctrl)
			}
		}
	} else if len(s.VideoMapNames) == 0 {
		e.ErrorString("Must specify either \"controller_configs\" or \"stars_maps\"")
	}

	if s.Range == 0 {
		s.Range = 50
	}
	if s.HandoffAcceptFlashDuration == 0 {
		s.HandoffAcceptFlashDuration = 5
	}

	for name, rs := range s.RadarSites {
		e.Push("Radar site " + name)
		if p, ok := sg.Locate(rs.PositionString); rs.PositionString == "" || !ok {
			e.ErrorString("radar site position %q not found", rs.PositionString)
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
	if s.PDB.DisplayCustomSPCs && len(s.CustomSPCs) == 0 {
		e.ErrorString("\"display_custom_spcs\" was set but none were defined in \"custom_spcs\".")
	}

	disp := make(map[string]interface{})
	if s.Scratchpad1.DisplayExitFix {
		disp["display_exit_fix"] = nil
	}
	if s.Scratchpad1.DisplayExitFix1 {
		disp["display_exit_fix_1"] = nil
	}
	if s.Scratchpad1.DisplayExitGate {
		disp["display_exit_gate"] = nil
	}
	if s.Scratchpad1.DisplayAltExitGate {
		disp["display_alternate_exit_gate"] = nil
	}
	if len(disp) > 1 {
		d := util.SortedMapKeys(disp)
		d = util.MapSlice(d, func(s string) string { return `"` + s + `"` })
		e.ErrorString("Cannot specify " + strings.Join(d, " and ") + "for \"scratchpad1\"")
	}

	for _, spc := range s.CustomSPCs {
		if len(spc) != 2 || spc[0] < 'A' || spc[0] > 'Z' || spc[1] < 'A' || spc[1] > 'Z' {
			e.ErrorString("Invalid \"custom_spcs\" code %q: must be two characters between A-Z", spc)
		}
		if av.StringIsSPC(spc) {
			e.ErrorString("%q is a standard SPC already", spc)
		}
	}

	// Significant points
	e.Push("\"significant_points\"")
	for name, sp := range s.SignificantPoints {
		e.Push(name)

		if len(name) < 3 {
			e.ErrorString("name must be at least 3 characters")
		} else {
			sp.Name = name

			if sp.ShortName != "" && len(name) == 3 {
				e.ErrorString("\"short_name\" can only be given if name is more than 3 characters.")
			}
			if len(sp.ShortName) > 3 {
				e.ErrorString("\"short_name\" cannot be more than 3 characters.")
			}
			if sp.Location.IsZero() {
				if p, ok := sg.Locate(name); !ok {
					e.ErrorString("unable to find location of %q", name)
				} else {
					sp.Location = p
				}
			}
		}

		// Update for any changes we made
		s.SignificantPoints[name] = sp

		e.Pop()
	}
	e.Pop()

	// Altimeters
	if len(s.Altimeters) > 6 {
		e.ErrorString("Only 6 airports may be specified for \"altimeters\"; %d were given", len(s.Altimeters))
	}
	for _, ap := range s.Altimeters {
		if _, ok := sg.Airports[ap]; !ok {
			e.ErrorString("Airport %q in \"altimeters\" not found in scenario group \"airports\"", ap)
		}
	}

	// Hold for release validation
	for airport, ap := range sg.Airports {
		var matches []string
		for _, list := range s.CoordinationLists {
			if slices.Contains(list.Airports, airport) {
				matches = append(matches, list.Name)
			}
		}

		if ap.HoldForRelease {
			// Make sure it's in either zero or one of the coordination lists.
			if len(matches) > 1 {
				e.ErrorString("Airport %q is in multiple entries in \"coordination_lists\": %s.", airport, strings.Join(matches, ", "))
			}
		} else if len(matches) != 0 {
			// And it shouldn't be any if it's not hold for release
			e.ErrorString("Airport %q isn't \"hold_for_release\" but is in \"coordination_lists\": %s.", airport,
				strings.Join(matches, ", "))
		}
	}

	if s.MonitoredBeaconCodeBlocksString == nil {
		s.MonitoredBeaconCodeBlocks = []av.Squawk{0o12} // 12xx block by default
	} else {
		for _, bl := range strings.Split(*s.MonitoredBeaconCodeBlocksString, ",") {
			bl = strings.TrimSpace(bl)
			if code, err := av.ParseSquawkOrBlock(bl); err != nil {
				e.ErrorString("invalid beacon code %q in \"beacon_code_blocks\": %v", bl, err)
			} else {
				s.MonitoredBeaconCodeBlocks = append(s.MonitoredBeaconCodeBlocks, code)
			}
		}
	}

	seenIds := make(map[string][]string)
	for _, list := range s.CoordinationLists {
		e.Push("\"coordination_lists\" " + list.Name)

		if list.Name == "" {
			e.ErrorString("\"name\" must be specified for coordination list.")
		}
		if list.Id == "" {
			e.ErrorString("\"id\" must be specified for coordination list.")
		}
		if len(list.Airports) == 0 {
			e.ErrorString("At least one airport must be specified in \"airports\" for coordination list.")
		}

		seenIds[list.Id] = append(seenIds[list.Id], list.Name)

		// Make sure all airport names in coordination lists are part of the scenario.
		for _, ap := range list.Airports {
			if _, ok := sg.Airports[ap]; !ok {
				e.ErrorString("Airport %q not defined in scenario group.", ap)
			}
		}

		e.Pop()
	}
	// Make sure that no two coordination lists have the same id.
	for id, groups := range seenIds {
		if len(groups) > 1 {
			e.ErrorString("Multiple \"coordination_lists\" are using id %q: %s", id, strings.Join(groups, ", "))
		}
	}

	if len(s.VideoMapNames) == 0 {
		if len(s.ControllerConfigs) == 0 {
			e.ErrorString("must provide one of \"stars_maps\" or \"controller_configs\" in \"stars_config\"")
		}
		var err error
		s.ControllerConfigs, err = util.CommaKeyExpand(s.ControllerConfigs)
		if err != nil {
			e.Error(err)
		}
	} else if len(s.ControllerConfigs) > 0 {
		e.ErrorString("cannot provide both \"stars_maps\" and \"controller_configs\" in \"stars_config\"")
	}

	for _, aa := range s.AirspaceAwareness {
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
				e.ErrorString("%q: invalid \"aircraft_type\". Expected \"J\", \"T\", or \"P\".", t)
			}
		}
	}

	e.Push("\"restriction_areas\"")
	if len(s.RestrictionAreas) > av.MaxRestrictionAreas {
		e.ErrorString("No more than %d restriction areas may be specified; %d were given.",
			av.MaxRestrictionAreas, len(s.RestrictionAreas))
	}
	for idx := range s.RestrictionAreas {
		ra := &s.RestrictionAreas[idx]

		// General checks
		if ra.Title == "" {
			e.ErrorString("Must define \"title\" for restriction area.")
		}
		for i := range 2 {
			if len(ra.Text[i]) > 32 {
				e.ErrorString("Maximum of 32 characters per line in \"text\": line %d: %q (%d)",
					i, ra.Text, len(ra.Text[i]))
			}
		}
		if ra.Closed && len(ra.Vertices) == 0 || len(ra.Vertices[0]) < 3 {
			e.ErrorString("At least 3 \"vertices\" must be given for a closed restriction area.")
		}
		if !ra.Closed && len(ra.Vertices) == 0 || len(ra.Vertices[0]) < 2 {
			e.ErrorString("At least 2 \"vertices\" must be given for an open restriction area.")
		}
		if ra.Color < 0 || ra.Color > 8 {
			// (We allow 0 for unset and treat it as 1 when we draw.)
			e.ErrorString("\"color\" must be between 1 and 8 (inclusive).")
		}

		if len(ra.VerticesUser) > 0 {
			// Polygons
			ra.VerticesUser = ra.VerticesUser.InitializeLocations(sg, sg.NmPerLongitude, sg.MagneticVariation, false, e)
			var verts []math.Point2LL
			for _, v := range ra.VerticesUser {
				verts = append(verts, v.Location)
			}

			ra.Vertices = make([][]math.Point2LL, 1)
			ra.Vertices[0] = verts
			ra.UpdateTriangles()

			if ra.TextPosition.IsZero() {
				ra.TextPosition = ra.AverageVertexPosition()
			}

			if ra.CircleRadius > 0 {
				e.ErrorString("Cannot specify both \"circle_radius\" and \"vertices\".")
			}
		} else if ra.CircleRadius > 0 {
			// Circle-related checks
			if ra.CircleRadius > 125 {
				e.ErrorString("\"radius\" cannot be larger than 125.")
			}
			if ra.CircleCenter.IsZero() {
				e.ErrorString("Must specify \"circle_center\" if \"circle_radius\" is given.")
			}
			if ra.TextPosition.IsZero() {
				ra.TextPosition = ra.CircleCenter
			}
		} else {
			// Must be text-only
			if ra.Text[0] != "" || ra.Text[1] != "" && ra.TextPosition.IsZero() {
				e.ErrorString("Must specify \"text_position\" with restriction area")
			}
		}

		if ra.Shaded && ra.CircleRadius == 0 && len(ra.Vertices) == 0 {
			e.ErrorString("\"shaded\" cannot be specified without \"circle_radius\" or \"vertices\".")
		}
	}
	e.Pop()

	e.Pop() // stars_config
}

func initializeSimConfigurations(sg *ScenarioGroup,
	simConfigurations map[string]map[string]*Configuration, multiController bool, e *util.ErrorLogger) {
	config := &Configuration{
		ScenarioConfigs:  make(map[string]*SimScenarioConfiguration),
		ControlPositions: sg.ControlPositions,
		DefaultScenario:  sg.DefaultScenario,
	}

	vfrAirports := make(map[string]*av.Airport)
	for name, ap := range sg.Airports {
		if ap.VFRRateSum() > 0 {
			vfrAirports[name] = ap
		}
	}
	for name, scenario := range sg.Scenarios {
		lc := sim.MakeLaunchConfig(scenario.DepartureRunways, *scenario.VFRRateScale, vfrAirports,
			scenario.InboundFlowDefaultRates)
		sc := &SimScenarioConfiguration{
			SplitConfigurations: scenario.SplitConfigurations,
			LaunchConfig:        lc,
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
// LoadScenarioGroups

func loadScenarioGroup(filesystem fs.FS, path string, e *util.ErrorLogger) *ScenarioGroup {
	e.Push("File " + path)
	defer e.Pop()

	contents, err := fs.ReadFile(filesystem, path)
	if err != nil {
		e.Error(err)
		return nil
	}

	util.CheckJSON[ScenarioGroup](contents, e)
	if e.HaveErrors() {
		return nil
	}

	var s ScenarioGroup
	if err := util.UnmarshalJSONBytes(contents, &s); err != nil {
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

// LoadScenarioGroups loads all of the available scenarios, both from the
// scenarios/ directory in the source code distribution as well as,
// optionally, a scenario file provided on the command line.  It doesn't
// try to do any sort of meaningful error handling but it does try to
// continue on in the presence of errors; all errors will be printed and
// the program will exit if there are any.  We'd rather force any errors
// due to invalid scenario definitions to be fixed...
func LoadScenarioGroups(isLocal bool, extraScenarioFilename string, extraVideoMapFilename string,
	e *util.ErrorLogger, lg *log.Logger) (map[string]map[string]*ScenarioGroup, map[string]map[string]*Configuration, map[string]*sim.VideoMapManifest) {
	start := time.Now()

	// First load the scenarios.
	scenarioGroups := make(map[string]map[string]*ScenarioGroup)
	simConfigurations := make(map[string]map[string]*Configuration)

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
				return util.RootFS{}
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
		}
	}

	// Next load the video map manifests so we can validate the map references in scenarios.
	mapManifests := make(map[string]*sim.VideoMapManifest)
	err = util.WalkResources("videomaps", func(path string, d fs.DirEntry, fs fs.FS, err error) error {
		if err != nil {
			lg.Errorf("error walking videomaps: %v", err)
			return nil
		}

		if d.IsDir() {
			return nil
		}

		if strings.HasSuffix(path, "-videomaps.gob") || strings.HasSuffix(path, "-videomaps.gob.zst") {
			mapManifests[path], err = sim.LoadVideoMapManifest(path)
		}

		return err
	})
	if err != nil {
		lg.Errorf("error loading videomaps: %v", err)
		os.Exit(1)
	}

	lg.Infof("scenario/video map manifest load time: %s\n", time.Since(start))

	// Load the video map specified on the command line, if any.
	if extraVideoMapFilename != "" {
		mapManifests[extraVideoMapFilename], err = sim.LoadVideoMapManifest(extraVideoMapFilename)
		if err != nil {
			lg.Errorf("%s: %v", extraVideoMapFilename, err)
			os.Exit(1)
		}
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
					e.ErrorString("scenario %q is also defined in the %q scenario group",
						scenarioName, other)
				}
				scenarioNames[scenarioName] = groupName
			}

			// Make sure we have what we need in terms of video maps
			fa := &sgroup.STARSFacilityAdaptation
			if vf := fa.VideoMapFile; vf == "" {
				e.ErrorString("no \"video_map_file\" specified")
			} else if manifest, ok := mapManifests[vf]; !ok {
				e.ErrorString("no manifest for video map %q found. Options: %s", vf,
					strings.Join(util.SortedMapKeys(mapManifests), ", "))
			} else {
				multiController := !isLocal
				sgroup.PostDeserialize(multiController, e, simConfigurations, manifest)
			}

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
						for _, ac := range al.Aircraft() {
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

	return scenarioGroups, simConfigurations, mapManifests
}

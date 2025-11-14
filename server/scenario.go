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

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/brunoga/deep"
)

type scenarioGroup struct {
	ARTCC              string                     `json:"artcc" scope:"eram"`
	Area               string                     `json:"area" scope:"eram"`
	TRACON             string                     `json:"tracon" scope:"stars"`
	Name               string                     `json:"name"`
	Airports           map[string]*av.Airport     `json:"airports"`
	Fixes              map[string]math.Point2LL   `json:"-"`
	FixesStrings       util.OrderedMap            `json:"fixes"`
	Scenarios          map[string]*scenario       `json:"scenarios"`
	DefaultScenario    string                     `json:"default_scenario"`
	ControlPositions   map[string]*av.Controller  `json:"control_positions"`
	Airspace           av.Airspace                `json:"airspace"`
	InboundFlows       map[string]*av.InboundFlow `json:"inbound_flows"`
	VFRReportingPoints []av.VFRReportingPoint     `json:"vfr_reporting_points"`

	PrimaryAirport string `json:"primary_airport" scope:"stars"`

	ReportingPointStrings []string            `json:"reporting_points"`
	ReportingPoints       []av.ReportingPoint // not in JSON

	NmPerLatitude      float32 // Always 60
	NmPerLongitude     float32 // Derived from Center
	MagneticVariation  float32
	MagneticAdjustment float32                `json:"magnetic_adjustment"`
	FacilityAdaptation sim.FacilityAdaptation `json:"stars_config"`
}

type scenario struct {
	SoloController      string                   `json:"solo_controller"`
	SplitConfigurations av.SplitConfigurationSet `json:"multi_controllers"`
	DefaultSplit        string                   `json:"default_split"`
	VirtualControllers  []string                 `json:"controllers"`

	WindSpecifier *wx.WindSpecifier `json:"wind,omitempty"`

	// Map from inbound flow names to a map from airport name to default rate,
	// with "overflights" a special case to denote overflights
	InboundFlowDefaultRates map[string]map[string]int `json:"inbound_rates"`
	// Temporary backwards compatibility
	ArrivalGroupDefaultRates map[string]map[string]int `json:"arrivals"`

	Airspace map[string][]string `json:"airspace"`

	DepartureRunways []sim.DepartureRunway `json:"departure_runways,omitempty"`
	ArrivalRunways   []sim.ArrivalRunway   `json:"arrival_runways,omitempty"`

	Center          math.Point2LL `json:"-"`
	CenterString    string        `json:"center"`
	Range           float32       `json:"range"`
	DefaultMaps     []string      `json:"default_maps"`
	DefaultMapGroup string        `json:"default_map_group" scope:"eram"`
	VFRRateScale    *float32      `json:"vfr_rate_scale"`
}

func (s *scenario) PostDeserialize(sg *scenarioGroup, e *util.ErrorLogger, manifest *sim.VideoMapManifest) {
	defer e.CheckDepth(e.CurrentDepth())

	// Validate wind specifier if present
	if s.WindSpecifier != nil {
		e.Push("\"wind\"")
		if err := s.WindSpecifier.Validate(); err != nil {
			e.Error(err)
		}
		e.Pop()
	}

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

	// Auto-populate SplitConfigurations for single-controller scenarios
	if s.SoloController != "" && len(s.SplitConfigurations) == 0 {
		// Collect departure airports that don't have virtual controllers
		// (airports with virtual controllers are handled by those
		// controllers, not the human).
		departures := make(map[string]bool)
		for _, rwy := range s.DepartureRunways {
			if ap, ok := sg.Airports[rwy.Airport]; ok && ap.DepartureController == "" {
				departures[rwy.Airport] = true
			}
		}
		departureList := slices.Collect(maps.Keys(departures))

		// Collect inbound flows that have handoffs to human controllers.
		var inboundFlows []string
		for flowName := range s.InboundFlowDefaultRates {
			if flow, ok := sg.InboundFlows[flowName]; ok {
				// Check if this flow has arrivals or overflights with handoffs
				hasHandoff := len(flow.Arrivals) > 0
				if !hasHandoff {
					// Check if any overflight has a human handoff
					hasHandoff = slices.ContainsFunc(flow.Overflights, func(of av.Overflight) bool {
						return slices.ContainsFunc(of.Waypoints, func(wp av.Waypoint) bool {
							return wp.HumanHandoff
						})
					})
				}
				if hasHandoff {
					inboundFlows = append(inboundFlows, flowName)
				}
			}
		}

		// Create the default split configuration
		s.SplitConfigurations = av.SplitConfigurationSet{
			"default": av.SplitConfiguration{
				s.SoloController: &av.MultiUserController{
					Primary:      true,
					Departures:   departureList,
					InboundFlows: inboundFlows,
				},
			},
		}
		s.DefaultSplit = "default"
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
	for _, aa := range sg.FacilityAdaptation.AirspaceAwareness {
		addController(aa.ReceivingController)
	}

	airportExits := make(map[string]map[string]interface{}) // airport -> exit -> is it covered
	for _, rwy := range s.DepartureRunways {
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

			if !util.SeqContainsFunc(maps.Values(ap.Approaches),
				func(appr *av.Approach) bool { return appr.Runway == rwy.Runway }) {
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
				exitRoutes := ap.DepartureRoutes[rwy.Runway]
				for fix, route := range exitRoutes {
					if rwy.Category == "" || ap.ExitCategories[fix] == rwy.Category {
						if activeAirportSIDs[rwy.Airport] == nil {
							activeAirportSIDs[rwy.Airport] = make(map[string]interface{})
						}
						if activeAirportRunways[rwy.Airport] == nil {
							activeAirportRunways[rwy.Airport] = make(map[string]interface{})
						}
						if route.DepartureController != "" {
							if _, ok := sg.ControlPositions[route.DepartureController]; !ok {
								e.ErrorString("controller %q for departure route %q is unknown", route.DepartureController, fix)
							} else if !slices.Contains(s.VirtualControllers, route.DepartureController) {
								e.ErrorString("controller %q for departure route %q is not a virtual controller", route.DepartureController, fix)
							}
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
			} else {
				// Validate that default airport exists
				if _, ok := sg.Airports[ctrl.DefaultAirport]; !ok {
					e.ErrorString("%s: default airport %q is not included in scenario", ctrl.Position, ctrl.DefaultAirport)
				}
			}
		}
		for _, callsign := range s.SplitConfigurations.Splits() {
			if ctrl, ok := sg.ControlPositions[callsign]; ok {
				if ctrl.DefaultAirport == "" {
					e.ErrorString("%s: controller must have \"default_airport\" specified (required for CRDA).", ctrl.Position)
				} else {
					if _, ok := sg.Airports[ctrl.DefaultAirport]; !ok {
						e.ErrorString("%s: default airport %q is not included in scenario", ctrl.Position, ctrl.DefaultAirport)
					}
				}
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

	for name := range util.SortedMap(s.InboundFlowDefaultRates) {
		e.Push("Inbound flow " + name)
		// Make sure the inbound flow has been defined
		if flow, ok := sg.InboundFlows[name]; !ok {
			e.ErrorString("inbound flow not found")
		} else {
			for _, ar := range flow.Arrivals {
				addController(ar.InitialController)
				addControllersFromWaypoints(ar.Waypoints)
				for _, rwys := range ar.RunwayWaypoints {
					for _, rwyWps := range rwys {
						addControllersFromWaypoints(rwyWps)
					}
				}
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

	for _, dm := range s.DefaultMaps {
		if !manifest.HasMap(dm) {
			e.ErrorString("video map %q in \"default_maps\" not found. Use -listmaps "+
				"<path to Zxx-videomaps.gob.zst> to show available video maps for an ARTCC.", dm)
		}
	}

	if sg.ARTCC != "" {
		if !manifest.HasMapGroup(s.DefaultMapGroup) {
			e.ErrorString("video map group %q in \"default_map_group\" not found. Use -listmaps "+
				"<path to Zxx-videomaps.gob.zst> to show available video map groups for an ARTCC.", s.DefaultMapGroup)
		}
	}

	if s.VFRRateScale == nil { // unspecified -> default to 1
		one := float32(1)
		s.VFRRateScale = &one
	}
}

///////////////////////////////////////////////////////////////////////////
// ScenarioGroup

func (sg *scenarioGroup) Locate(s string) (math.Point2LL, bool) {
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

func (sg *scenarioGroup) Similar(fix string) []string {
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

func (sg *scenarioGroup) PostDeserialize(e *util.ErrorLogger, simConfigurations map[string]map[string]*Configuration,
	manifest *sim.VideoMapManifest) {
	defer e.CheckDepth(e.CurrentDepth())

	// Rewrite legacy files to be TCP-based.
	sg.rewriteControllers(e)

	// stars_config items. This goes first because we need to initialize
	// Center (and thence NmPerLongitude) ASAP.

	// Airports that (may) have controlled controlled departures or
	// arrivals; we determine this by checking if they're in B, C, or D
	// airspace, which is probably sufficient?
	controlledAirports := slices.Collect(
		util.Seq2Keys(
			util.FilterSeq2(maps.All(sg.Airports), func(name string, ap *av.Airport) bool {
				return len(ap.Departures) > 0 || len(ap.Approaches) > 0
			})))
	allAirports := slices.Collect(maps.Keys(sg.Airports))

	sg.FacilityAdaptation.PostDeserialize(sg, controlledAirports, allAirports, e)

	sg.NmPerLatitude = 60
	sg.NmPerLongitude = math.NMPerLongitudeAt(sg.FacilityAdaptation.Center)

	if sg.ARTCC == "" {
		if sg.TRACON == "" {
			e.ErrorString("\"tracon\" or must be specified")
		} else if _, ok := av.DB.TRACONs[sg.TRACON]; !ok {
			e.ErrorString("TRACON %q is unknown; it must be a 3-letter identifier listed at "+
				"https://www.faa.gov/about/office_org/headquarters_offices/ato/service_units/air_traffic_services/tracon.",
				sg.TRACON)
		}
	} else if sg.TRACON == "" {
		if sg.ARTCC == "" {
			e.ErrorString("\"artcc\" must be specified")
		}
		if _, ok := av.DB.ARTCCs[sg.ARTCC]; !ok {
			e.ErrorString("ARTCC %q is unknown; it must be a 3-letter identifier listed at "+
				"https://www.faa.gov/about/office_org/headquarters_offices/ato/service_units/air_traffic_services/artcc", sg.ARTCC)
		}
		sg.TRACON = sg.ARTCC // TODO: find a better way to do this
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
				e.ErrorString("base fix %q unknown", strs[1])
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

	PostDeserializeFacilityAdaptation(&sg.FacilityAdaptation, e, sg, manifest)

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
	if sg.ARTCC == "" {
		if sg.PrimaryAirport == "" {
			e.ErrorString("\"primary_airport\" not specified")
		} else if ap, ok := av.DB.Airports[sg.PrimaryAirport]; !ok {
			e.ErrorString("\"primary_airport\" %q unknown", sg.PrimaryAirport)
		} else if mvar, err := av.DB.MagneticGrid.Lookup(ap.Location); err != nil {
			e.ErrorString("%s: unable to find magnetic declination: %v", sg.PrimaryAirport, err)
		} else {
			sg.MagneticVariation = mvar + sg.MagneticAdjustment
		}
	} else if mvar, err := av.DB.MagneticGrid.Lookup(sg.FacilityAdaptation.Center); err != nil {
		e.ErrorString("%s: unable to find magnetic declination: %v", sg.ARTCC, err)
	} else {
		sg.MagneticVariation = mvar + sg.MagneticAdjustment
	}

	if len(sg.Airports) == 0 {
		e.ErrorString("No \"airports\" specified in scenario group")
	}
	for name, ap := range sg.Airports {
		e.Push("Airport " + name)
		ap.PostDeserialize(name, sg, sg.NmPerLongitude, sg.MagneticVariation,
			sg.ControlPositions, sg.FacilityAdaptation.Scratchpads, sg.Airports,
			sg.FacilityAdaptation.CheckScratchpad, e)
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
		if ctrl.ERAMFacility && sg.ARTCC == "" {
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

		// Validate default airport if specified
		if ctrl.DefaultAirport != "" {
			if _, ok := sg.Airports[ctrl.DefaultAirport]; !ok {
				e.ErrorString("\"default_airport\" %q is not an airport in this scenario", ctrl.DefaultAirport)
			}
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
				sg.Airports, sg.ControlPositions, sg.FacilityAdaptation.CheckScratchpad, e)
		}
		for i := range flow.Overflights {
			flow.Overflights[i].PostDeserialize(sg, sg.NmPerLongitude, sg.MagneticVariation,
				sg.Airports, sg.ControlPositions, sg.FacilityAdaptation.CheckScratchpad, e)
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

	for i := range sg.VFRReportingPoints {
		sg.VFRReportingPoints[i].PostDeserialize(sg, sg.ControlPositions, e)
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

	initializeSimConfigurations(sg, simConfigurations, e)
}

func (sg *scenarioGroup) rewriteControllers(e *util.ErrorLogger) {
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

	fa := &sg.FacilityAdaptation
	for i := range fa.AirspaceAwareness {
		rewrite(&fa.AirspaceAwareness[i].ReceivingController)
	}
	for position, config := range fa.ControllerConfigs {
		// Rewrite controller
		delete(fa.ControllerConfigs, position)
		rewrite(&position)
		fa.ControllerConfigs[position] = config
	}

	for _, flow := range sg.InboundFlows {
		for i := range flow.Arrivals {
			rewrite(&flow.Arrivals[i].InitialController)
			rewriteWaypoints(flow.Arrivals[i].Waypoints)
			for _, rwyWps := range flow.Arrivals[i].RunwayWaypoints {
				for _, wps := range rwyWps {
					rewriteWaypoints(wps)
				}
			}
		}
		for i := range flow.Overflights {
			rewrite(&flow.Overflights[i].InitialController)
			rewriteWaypoints(flow.Overflights[i].Waypoints)
		}
	}

	sg.ControlPositions = pos
}

func PostDeserializeFacilityAdaptation(s *sim.FacilityAdaptation, e *util.ErrorLogger, sg *scenarioGroup,
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

		for tcp, config := range s.ControllerConfigs {
			for _, name := range config.DefaultMaps {
				if !slices.Contains(config.VideoMapNames, name) {
					e.ErrorString("default map %q for %q is not included in the controller's "+
						"\"video_maps\"", name, tcp)
				}

				if !manifest.HasMap(name) {
					e.ErrorString("video map %q in \"default_maps\" for controller %q is not a valid video map",
						name, tcp)
				}
			}
			for _, name := range config.VideoMapNames {
				if name != "" && !manifest.HasMap(name) {
					e.ErrorString("video map %q in \"video_maps\" for controller %q is not a valid video map",
						name, tcp)
				}
			}

			if ctrl, ok := sg.ControlPositions[tcp]; !ok {
				e.ErrorString("Control position %q in \"controller_configs\" not defined in \"control_positions\"", tcp)
			} else if ctrl.IsExternal() {
				e.ErrorString("Control position %q in \"controller_configs\" is external and not in this TRACON.", tcp)
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
		e.ErrorString("Cannot specify %s for \"scratchpad1\"", strings.Join(d, " and "))
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
	if s.SignificantPoints == nil {
		s.SignificantPoints = make(map[string]sim.SignificantPoint)
	}
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

	if s.UntrackedPositionSymbolOverrides.CodeRangesString != "" {
		e.Push("untracked_position_symbol_overrides")
		for c := range strings.SplitSeq(s.UntrackedPositionSymbolOverrides.CodeRangesString, ",") {
			low, high, ok := strings.Cut(c, "-")

			var err error
			var r [2]av.Squawk
			r[0], err = av.ParseSquawk(low)
			if err != nil {
				e.ErrorString("invalid beacon code %q in \"beacon_codes\": %v", low, err)
			} else if ok {
				r[1], err = av.ParseSquawk(high)
				if err != nil {
					e.ErrorString("invalid beacon code %q in \"beacon_codes\": %v", high, err)
				} else if r[0] > r[1] {
					e.ErrorString("first code %q in range must be less than or equal to second %q", low, high)
				}
			} else {
				r[1] = r[0]
			}
			s.UntrackedPositionSymbolOverrides.CodeRanges = append(s.UntrackedPositionSymbolOverrides.CodeRanges, r)
		}

		if len(s.UntrackedPositionSymbolOverrides.Symbol) == 0 {
			e.ErrorString("\"symbol\" must be provided if \"untracked_position_symbol_overrides\" is specified")
		} else if len(s.UntrackedPositionSymbolOverrides.Symbol) > 1 {
			e.ErrorString("only one character may be provided for \"symbol\"")
		}
		e.Pop()
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
			e.ErrorString("must provide one of \"stars_maps\" or \"controller_configs\" with \"video_maps\" in \"stars_config\"")
		}
		var err error
		s.ControllerConfigs, err = util.CommaKeyExpand(s.ControllerConfigs)
		if err != nil {
			e.Error(err)
		}
	}

	for _, aa := range s.AirspaceAwareness {
		for _, fix := range aa.Fix {
			if _, ok := sg.Locate(fix); !ok && fix != "ALL" {
				e.ErrorString("%s : fix unknown", fix)
			}
		}

		if aa.AltitudeRange[0] > aa.AltitudeRange[1] {
			e.ErrorString("lower end of \"altitude_range\" %d above upper end %d",
				aa.AltitudeRange[0], aa.AltitudeRange[1])
		}

		if _, ok := sg.ControlPositions[aa.ReceivingController]; !ok {
			e.ErrorString("%s: controller unknown", aa.ReceivingController)
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

func initializeSimConfigurations(sg *scenarioGroup, simConfigurations map[string]map[string]*Configuration, e *util.ErrorLogger) {
	facility := sg.TRACON
	if facility == "" {
		facility = sg.ARTCC
	}
	artcc := sg.ARTCC
	if artcc == "" {
		if info, ok := av.DB.TRACONs[facility]; ok {
			artcc = info.ARTCC
		}
	}

	config := &Configuration{
		ScenarioConfigs:  make(map[string]*SimScenarioConfiguration),
		ControlPositions: sg.ControlPositions,
		DefaultScenario:  sg.DefaultScenario,
		Facility:         facility,
		ARTCC:            artcc,
		Area:             sg.Area,
	}

	vfrAirports := make(map[string]*av.Airport)
	for name, ap := range sg.Airports {
		if ap.VFRRateSum() > 0 {
			vfrAirports[name] = ap
		}
	}
	for name, scenario := range sg.Scenarios {
		haveVFRReportingRegions := util.SeqContainsFunc(maps.Values(sg.FacilityAdaptation.ControllerConfigs),
			func(cc *sim.STARSControllerConfig) bool { return len(cc.FlightFollowingAirspace) > 0 })
		lc := sim.MakeLaunchConfig(scenario.DepartureRunways, *scenario.VFRRateScale, vfrAirports,
			scenario.InboundFlowDefaultRates, haveVFRReportingRegions)
		sc := &SimScenarioConfiguration{
			SplitConfigurations: scenario.SplitConfigurations,
			LaunchConfig:        lc,
			DepartureRunways:    scenario.DepartureRunways,
			ArrivalRunways:      scenario.ArrivalRunways,
			PrimaryAirport:      sg.PrimaryAirport,
			MagneticVariation:   sg.MagneticVariation,
			WindSpecifier:       scenario.WindSpecifier,
		}

		// All scenarios now have SplitConfigurations (auto-populated if
		// needed if they were defined as single-controller).
		var err error
		sc.SelectedController, err = scenario.SplitConfigurations.GetPrimaryController(scenario.DefaultSplit)
		if err != nil {
			e.Error(err)
		}
		sc.SelectedSplit = scenario.DefaultSplit

		config.ScenarioConfigs[name] = sc
	}

	if len(config.ScenarioConfigs) > 0 {
		if simConfigurations[facility] == nil {
			simConfigurations[facility] = make(map[string]*Configuration)
		}
		simConfigurations[facility][sg.Name] = config
	}
}

///////////////////////////////////////////////////////////////////////////
// LoadScenarioGroups

func loadScenarioGroup(filesystem fs.FS, path string, e *util.ErrorLogger) *scenarioGroup {
	e.Push("File " + path)
	defer e.Pop()

	contents, err := fs.ReadFile(filesystem, path)
	if err != nil {
		e.Error(err)
		return nil
	}

	util.CheckJSON[scenarioGroup](contents, e)
	if e.HaveErrors() {
		return nil
	}

	var s scenarioGroup
	if err := util.UnmarshalJSONBytes(contents, &s); err != nil {
		e.Error(err)
		return nil
	}
	if s.Name == "" {
		e.ErrorString("scenario group is missing \"name\"")
		return nil
	}
	if s.TRACON == "" && s.ARTCC == "" {
		e.ErrorString("scenario group is missing \"tracon\" or \"artcc\"")
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
//
// Returns: scenarioGroups, simConfigurations, mapManifests, extraScenarioErrors
// If the extra scenario file has errors, they are returned in extraScenarioErrors
// and that scenario is not loaded, but execution continues.
func LoadScenarioGroups(extraScenarioFilename string, extraVideoMapFilename string,
	e *util.ErrorLogger, lg *log.Logger) (map[string]map[string]*scenarioGroup, map[string]map[string]*Configuration, map[string]*sim.VideoMapManifest, string) {
	start := time.Now()

	// First load the scenarios.
	scenarioGroups := make(map[string]map[string]*scenarioGroup)
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

		s := loadScenarioGroup(fs, path, e)
		if s != nil {
			facility := util.Select(s.TRACON == "", s.ARTCC, s.TRACON)
			if _, ok := scenarioGroups[facility][s.Name]; ok {
				e.ErrorString("%s / %s: scenario redefined", s.TRACON, s.Name)
			} else {
				if scenarioGroups[facility] == nil {
					scenarioGroups[facility] = make(map[string]*scenarioGroup)
				}
				scenarioGroups[facility][s.Name] = s
			}
		}
		return nil
	})
	if err != nil {
		e.Error(err)
	}
	if e.HaveErrors() {
		// Don't keep going since we'll likely crash in the following
		return nil, nil, nil, ""
	}

	// Load the scenario specified on command line, if any.
	// Store it separately so we can validate it with a separate error logger
	var extraScenario *scenarioGroup
	var extraScenarioFacility string
	var extraScenarioErrors string
	if extraScenarioFilename != "" {
		var extraE util.ErrorLogger
		fs := func() fs.FS {
			if filepath.IsAbs(extraScenarioFilename) {
				return util.RootFS{}
			} else {
				return os.DirFS(".")
			}
		}()
		s := loadScenarioGroup(fs, extraScenarioFilename, &extraE)
		if s != nil {
			facility := util.Select(s.TRACON == "", s.ARTCC, s.TRACON)

			// These may have an empty "video_map_file" member, which
			// is automatically patched up here...
			if s.FacilityAdaptation.VideoMapFile == "" {
				if extraVideoMapFilename != "" {
					s.FacilityAdaptation.VideoMapFile = extraVideoMapFilename
				} else {
					extraE.ErrorString("%s: no \"video_map_file\" in scenario and -videomap not specified",
						extraScenarioFilename)
				}
			}

			// Store the scenario for later validation (don't add to scenarioGroups yet)
			if !extraE.HaveErrors() {
				extraScenario = s
				extraScenarioFacility = facility
			}
		}

		// Capture any errors from the extra scenario
		if extraE.HaveErrors() {
			extraScenarioErrors = extraE.String()
			lg.Warnf("Extra scenario file has errors and will not be loaded: %s", extraScenarioFilename)
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
			fa := &sgroup.FacilityAdaptation
			if vf := fa.VideoMapFile; vf == "" {
				e.ErrorString("no \"video_map_file\" specified")
			} else if manifest, ok := mapManifests[vf]; !ok {
				e.ErrorString("no manifest for video map %q found. Options: %s", vf,
					strings.Join(util.SortedMapKeys(mapManifests), ", "))
			} else { // if tracon
				sgroup.PostDeserialize(e, simConfigurations, manifest)
			}

			e.Pop()
		}
		e.Pop()
	}

	// Validate the extra scenario separately with its own error logger
	if extraScenario != nil {
		var extraE util.ErrorLogger
		extraE.Push("TRACON " + extraScenarioFacility)
		extraE.Push("Scenario group " + extraScenario.Name)

		// Make sure we have what we need in terms of video maps
		fa := &extraScenario.FacilityAdaptation
		if vf := fa.VideoMapFile; vf == "" {
			extraE.ErrorString("no \"video_map_file\" specified")
		} else if manifest, ok := mapManifests[vf]; !ok {
			extraE.ErrorString("no manifest for video map %q found. Options: %s", vf,
				strings.Join(util.SortedMapKeys(mapManifests), ", "))
		} else {
			extraScenario.PostDeserialize(&extraE, simConfigurations, manifest)
		}

		extraE.Pop()
		extraE.Pop()

		if extraE.HaveErrors() {
			extraScenarioErrors = extraE.String()
			lg.Warnf("Extra scenario file has validation errors and will not be loaded: %s", extraScenarioFilename)
		} else {
			// Only add to scenarioGroups if validation succeeded
			if scenarioGroups[extraScenarioFacility] == nil {
				scenarioGroups[extraScenarioFacility] = make(map[string]*scenarioGroup)
			}
			scenarioGroups[extraScenarioFacility][extraScenario.Name] = extraScenario
		}
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
	for t := range util.SortedMap(acTypes) {
		if av.DB.AircraftPerformance[t].Speed.V2 == 0 {
			missing = append(missing, t)
		}
	}
	lg.Infof("Missing V2 in performance database: %s", strings.Join(missing, ", "))

	return scenarioGroups, simConfigurations, mapManifests, extraScenarioErrors
}

// ListAllScenarios returns a sorted list of all available scenarios in TRACON/scenario format
func ListAllScenarios(scenarioFilename, videoMapFilename string, lg *log.Logger) ([]string, error) {
	var e util.ErrorLogger
	scenarioGroups, _, _, _ := LoadScenarioGroups(scenarioFilename, videoMapFilename, &e, lg)
	if e.HaveErrors() {
		return nil, fmt.Errorf("failed to load scenarios")
	}

	var scenarios []string
	for tracon, groups := range scenarioGroups {
		for _, group := range groups {
			for scenarioName := range group.Scenarios {
				scenarios = append(scenarios, tracon+"/"+scenarioName)
			}
		}
	}

	slices.Sort(scenarios)
	return scenarios, nil
}

// LookupScenario finds a scenario configuration by TRACON/scenario name
func LookupScenario(tracon, scenarioName string, scenarioGroups map[string]map[string]*scenarioGroup, configs map[string]map[string]*Configuration) (*Configuration, *scenarioGroup, error) {
	if groups, ok := scenarioGroups[tracon]; ok {
		for _, group := range groups {
			if _, ok := group.Scenarios[scenarioName]; ok {
				if cfgs, ok := configs[tracon]; ok {
					for _, cfg := range cfgs {
						if cfg.ScenarioConfigs[scenarioName] != nil {
							return cfg, group, nil
						}
					}
				}
			}
		}
	}
	return nil, nil, fmt.Errorf("scenario not found: %s/%s", tracon, scenarioName)
}

// CreateLaunchConfig creates a properly initialized LaunchConfig from scenario data
func CreateLaunchConfig(scenario *scenario, scenarioGroup *scenarioGroup) sim.LaunchConfig {
	// Create VFR airports map
	vfrAirports := make(map[string]*av.Airport)
	for name, ap := range scenarioGroup.Airports {
		if ap.VFRRateSum() > 0 {
			vfrAirports[name] = ap
		}
	}

	// Check for VFR reporting regions
	haveVFRReportingRegions := false
	for _, cfg := range scenarioGroup.FacilityAdaptation.ControllerConfigs {
		if cfg.FlightFollowingAirspace != nil {
			haveVFRReportingRegions = true
			break
		}
	}

	// Create proper LaunchConfig
	return sim.MakeLaunchConfig(
		scenario.DepartureRunways,
		util.Select(scenario.VFRRateScale == nil, 1.0, *scenario.VFRRateScale),
		vfrAirports,
		scenario.InboundFlowDefaultRates,
		haveVFRReportingRegions,
	)
}

// CreateNewSimConfiguration creates a NewSimConfiguration from scenario components
func CreateNewSimConfiguration(config *Configuration, scenarioGroup *scenarioGroup, scenarioName string) (*sim.NewSimConfiguration, error) {
	scenario, ok := scenarioGroup.Scenarios[scenarioName]
	if !ok {
		return nil, fmt.Errorf("scenario %s not found in group", scenarioName)
	}

	simConfig := config.ScenarioConfigs[scenarioName]
	if simConfig == nil {
		return nil, fmt.Errorf("scenario configuration %s not found", scenarioName)
	}

	newSimConfig := &sim.NewSimConfiguration{
		TRACON:             scenarioGroup.TRACON,
		Description:        scenarioName,
		LaunchConfig:       CreateLaunchConfig(scenario, scenarioGroup),
		DepartureRunways:   simConfig.DepartureRunways,
		ArrivalRunways:     simConfig.ArrivalRunways,
		PrimaryAirport:     simConfig.PrimaryAirport,
		Airports:           scenarioGroup.Airports,
		Fixes:              scenarioGroup.Fixes,
		VFRReportingPoints: scenarioGroup.VFRReportingPoints,
		ControlPositions:   scenarioGroup.ControlPositions,
		PrimaryController:  scenario.SoloController,
		SignOnPositions:    scenarioGroup.ControlPositions,
		InboundFlows:       scenarioGroup.InboundFlows,
		FacilityAdaptation: deep.MustCopy(scenarioGroup.FacilityAdaptation),
		ReportingPoints:    scenarioGroup.ReportingPoints,
		MagneticVariation:  scenarioGroup.MagneticVariation,
		NmPerLongitude:     scenarioGroup.NmPerLongitude,
		WindSpecifier:      scenario.WindSpecifier,
		Center:             util.Select(scenario.Center.IsZero(), scenarioGroup.FacilityAdaptation.Center, scenario.Center),
		Range:              util.Select(scenario.Range == 0, scenarioGroup.FacilityAdaptation.Range, scenario.Range),
		DefaultMaps:        scenario.DefaultMaps,
		DefaultMapGroup:    scenario.DefaultMapGroup,
		Airspace:           scenarioGroup.Airspace,
		ControllerAirspace: scenario.Airspace,
		VirtualControllers: scenario.VirtualControllers,
	}

	return newSimConfig, nil
}

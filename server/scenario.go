// pkg/server/scenario.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"encoding/json"
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
	Airspace           av.Airspace                `json:"airspace"`
	InboundFlows       map[string]*av.InboundFlow `json:"inbound_flows"`
	VFRReportingPoints []av.VFRReportingPoint     `json:"vfr_reporting_points"`

	PrimaryAirport string `json:"primary_airport" scope:"stars"`

	ReportingPointStrings []string            `json:"reporting_points"`
	ReportingPoints       []av.ReportingPoint // not in JSON

	NmPerLatitude      float32 // Always 60
	NmPerLongitude     float32 // Derived from Center
	MagneticVariation  float32
	MagneticAdjustment float32 `json:"magnetic_adjustment"`

	// The following fields are populated at runtime from the facility config file,
	// NOT from the scenario group JSON.
	ControlPositions   map[sim.TCP]*av.Controller `json:"-"`
	FacilityAdaptation sim.FacilityAdaptation     `json:"-"`
	HandoffTopology    *sim.HandoffTopology        `json:"-"`
	FixPairs           []sim.FixPairDefinition     `json:"-"`
}

type scenario struct {
	// ControllerConfiguration specifies which configuration from the facility
	// config to use for this scenario (via config_id).
	ControllerConfiguration *sim.ControllerConfiguration `json:"configuration"`

	// VirtualControllers is auto-derived at runtime from the facility config
	// and scenario routes; it is NOT read from JSON.
	VirtualControllers []sim.TCP `json:"-"`

	WindSpecifier *wx.WindSpecifier `json:"wind,omitempty"`

	// Map from inbound flow names to a map from airport name to default rate,
	// with "overflights" a special case to denote overflights
	InboundFlowDefaultRates map[string]map[string]int `json:"inbound_rates"`

	Airspace map[sim.TCP][]string `json:"airspace"`

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

	// Validate configuration
	if s.ControllerConfiguration == nil {
		e.ErrorString("\"configuration\" is required")
		return
	}

	// Resolve config_id to get assignments and consolidation from stars_config.configurations
	if s.ControllerConfiguration.ConfigId == "" {
		e.ErrorString("\"config_id\" must be specified in \"configuration\"")
	} else if config, ok := sg.FacilityAdaptation.Configurations[s.ControllerConfiguration.ConfigId]; !ok {
		e.ErrorString("\"config_id\" %q not found in \"stars_config\" \"configurations\"", s.ControllerConfiguration.ConfigId)
	} else {
		// Copy assignments from the referenced configuration
		s.ControllerConfiguration.InboundAssignments = maps.Clone(config.InboundAssignments)
		s.ControllerConfiguration.DepartureAssignments = maps.Clone(config.DepartureAssignments)
		// Only copy consolidation from facility config if the scenario doesn't provide its own override
		if len(s.ControllerConfiguration.DefaultConsolidation) == 0 {
			s.ControllerConfiguration.DefaultConsolidation = deep.MustCopy(config.DefaultConsolidation)
		}
	}

	// Auto-add airspace controllers to consolidation if they're valid
	// control positions but missing from the consolidation tree.
	if s.ControllerConfiguration != nil && len(s.ControllerConfiguration.DefaultConsolidation) > 0 {
		allPos := s.ControllerConfiguration.AllPositions()
		root, rootErr := s.ControllerConfiguration.DefaultConsolidation.RootPosition()
		if rootErr == nil {
			for ctrl := range s.Airspace {
				if !slices.Contains(allPos, ctrl) {
					if _, inFacility := sg.ControlPositions[ctrl]; inFacility {
						s.ControllerConfiguration.DefaultConsolidation[root] = append(
							s.ControllerConfiguration.DefaultConsolidation[root], ctrl)
					}
				}
			}
		}
	}

	s.ControllerConfiguration.Validate(sg.ControlPositions, e)

	// Validate inbound flow assignments
	if s.ControllerConfiguration != nil {
		// A flow only needs an inbound_assignment if it has a generic /ho
		// handoff (which doesn't specify a sector). Flows with no /ho at
		// all, or only specific handoffs like /ho1F, are exempt.
		flowNeedsHumanAssignment := func(flow *av.InboundFlow) bool {
			return flow.HasHumanHandoff()
		}

		// Check that every flow with generic /ho handoffs has an assignment.
		// Note: It is NOT an error if the configuration has excess assignments that the scenario doesn't use.
		for flowName := range s.InboundFlowDefaultRates {
			if flow, ok := sg.InboundFlows[flowName]; ok && flowNeedsHumanAssignment(flow) {
				if _, ok := s.ControllerConfiguration.InboundAssignments[flowName]; !ok {
					e.ErrorString("inbound flow %q needs human controller but has no assignment in \"inbound_assignments\"", flowName)
				}
			}
		}
		// departure_assignments validation is done below, after activeAirportSIDs/Runways maps are built
	}

	for ctrl, vnames := range s.Airspace {
		e.Push("airspace")

		// Verify controller is in configuration
		found := s.ControllerConfiguration != nil && slices.Contains(s.ControllerConfiguration.AllPositions(), ctrl)
		if !found {
			e.ErrorString("Controller %q not used in scenario", ctrl)
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

	// Auto-derive virtual controllers from routes, inbound flows, etc.
	// Every controller referenced must exist in sg.ControlPositions.
	humanPositionsSet := make(map[sim.TCP]bool)
	if s.ControllerConfiguration != nil {
		for _, pos := range s.ControllerConfiguration.AllPositions() {
			humanPositionsSet[pos] = true
		}
	}
	addController := func(tcp sim.TCP) {
		if tcp == "" {
			return
		}
		if _, ok := sg.ControlPositions[tcp]; !ok {
			e.ErrorString("controller %q referenced in route/flow but not defined in facility configuration", tcp)
			return
		}
		// Only add to VirtualControllers if not a human position
		if !humanPositionsSet[tcp] && !slices.Contains(s.VirtualControllers, tcp) {
			s.VirtualControllers = append(s.VirtualControllers, tcp)
		}
	}
	addControllersFromWaypoints := func(route []av.Waypoint) {
		for _, wp := range route {
			if wp.HandoffController != "" && wp.HandoffControllerFacility == "" {
				addController(sim.TCP(wp.HandoffController))
			}
		}
	}
	airportExits := make(map[string]map[string]any) // airport -> exit -> is it covered
	for _, rwy := range s.DepartureRunways {
		e.Push("Departure runway " + rwy.Airport + " " + rwy.Runway)

		if airportExits[rwy.Airport] == nil {
			airportExits[rwy.Airport] = make(map[string]any)
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

	activeAirports := make(map[*av.Airport]any) // all airports with departures or arrivals
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

	// Figure out which airports/runways and airports/SIDs are used in the scenario.
	activeAirportSIDs := make(map[string]map[string]any)
	activeAirportRunways := make(map[string]map[string]any)
	activeDepartureAirports := make(map[string]any)
	for _, rwy := range s.DepartureRunways {
		e.Push("departure runway " + rwy.Runway)

		ap, ok := sg.Airports[rwy.Airport]
		if !ok {
			e.ErrorString("%s: airport not found in \"airports\"", rwy.Airport)
		} else {
			activeAirports[ap] = nil
			activeDepartureAirports[rwy.Airport] = nil

			if ap.DepartureController != "" {
				// Only add to VirtualControllers if the controller is local (no departure_facility).
				if ap.DepartureFacility == "" {
					addController(sim.TCP(ap.DepartureController))
				}
			} else {
				// Only check for a human controller to be covering the track if there isn't
				// a virtual controller assigned to it.
				exitRoutes := ap.DepartureRoutes[rwy.Runway]
				for fix, route := range exitRoutes {
					fixCategory := ap.ExitCategories[fix]
					if rwy.Category != "" && fixCategory == "" {
						e.ErrorString("exit fix %q (SID %s) has no entry in \"exit_categories\" but runway uses category %q",
							fix, route.SID, rwy.Category)
					}
					if rwy.Category == "" || fixCategory == rwy.Category {
						if activeAirportSIDs[rwy.Airport] == nil {
							activeAirportSIDs[rwy.Airport] = make(map[string]any)
						}
						if activeAirportRunways[rwy.Airport] == nil {
							activeAirportRunways[rwy.Airport] = make(map[string]any)
						}
						if route.DepartureController != "" {
							// Only add to VirtualControllers if the controller is local (no departure_facility).
							if route.DepartureFacility == "" {
								addController(sim.TCP(route.DepartureController))
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

	// Validate departure_assignments - now that we have the activeAirportSIDs and activeAirportRunways maps
	// Note: Unlike arrivals/overflights, departures are handed to humans by default.
	// They only stay with virtual controllers if departure_controller is explicitly set.
	// activeAirportSIDs already filters out airports with departure_controller set.
	// Note: It is NOT an error if the configuration has excess assignments that the scenario doesn't use.
	if s.ControllerConfiguration != nil {
		// Track per-airport: assigned SIDs, assigned runways, and whether there's a fallback
		// Only track assignments that are relevant to THIS scenario's active airports/SIDs/runways
		assignedSIDs := make(map[string]map[string]any)    // airport -> set of SIDs
		assignedRunways := make(map[string]map[string]any) // airport -> set of runways
		hasAirportFallback := make(map[string]bool)        // airport -> has plain airport assignment

		for spec := range s.ControllerConfiguration.DepartureAssignments {
			ap, sidRunway, haveSIDRunway := strings.Cut(spec, "/")

			// Only process assignments for airports that are active in this scenario
			// and need human controller assignments (i.e., are in activeAirportSIDs)
			sids, isActiveHumanAirport := activeAirportSIDs[ap]
			if !isActiveHumanAirport {
				// Skip - either not an active departure airport, or has virtual controller
				continue
			}

			if haveSIDRunway {
				// Track assigned SIDs and runways per airport (only if active in this scenario)
				_, okSID := sids[sidRunway]
				_, okRunway := activeAirportRunways[ap][sidRunway]

				if okSID {
					if assignedSIDs[ap] == nil {
						assignedSIDs[ap] = make(map[string]any)
					}
					assignedSIDs[ap][sidRunway] = nil
				}
				if okRunway {
					if assignedRunways[ap] == nil {
						assignedRunways[ap] = make(map[string]any)
					}
					assignedRunways[ap][sidRunway] = nil
				}
				// Note: If neither okSID nor okRunway, this assignment is for a SID/runway
				// not active in this scenario, which is fine (excess assignments are OK)

				// Check for mixing SIDs and runways for this airport
				if len(assignedSIDs[ap]) > 0 && len(assignedRunways[ap]) > 0 {
					e.ErrorString("departure_assignments: cannot mix runways and SIDs as specifiers for airport %q in %q",
						ap, s.ControllerConfiguration.ConfigId)
				}
			} else {
				// Plain airport assignment (fallback)
				hasAirportFallback[ap] = true
			}
		}

		// Check that every active departure airport has complete coverage
		for ap, activeSIDs := range activeAirportSIDs {
			if hasAirportFallback[ap] {
				// Airport has a fallback, so incomplete SID/runway coverage is OK
				continue
			}

			if assigned, ok := assignedSIDs[ap]; ok {
				// Using SID-based assignments - check all active SIDs are covered
				for sid := range activeSIDs {
					if _, ok := assigned[sid]; !ok {
						e.ErrorString("departure_assignments: airport %q uses SID-based assignments but SID %q has no assignment in %q",
							ap, sid, s.ControllerConfiguration.ConfigId)
					}
				}
			} else if assigned, ok := assignedRunways[ap]; ok {
				// Using runway-based assignments - check all active runways are covered
				for rwy := range activeAirportRunways[ap] {
					if _, ok := assigned[rwy]; !ok {
						e.ErrorString("departure_assignments: airport %q uses runway-based assignments but runway %q has no assignment in %q",
							ap, rwy, s.ControllerConfiguration.ConfigId)
					}
				}
			} else {
				// No assignments at all for this airport
				e.ErrorString("departure airport %q has no assignment in \"departure_assignments\" in %q", ap,
					s.ControllerConfiguration.ConfigId)
			}
		}
	}

	// Validate area config default airports for CRDA
	for areaNum, ac := range sg.FacilityAdaptation.AreaConfigs {
		if ac.DefaultAirport != "" {
			if _, ok := sg.Airports[ac.DefaultAirport]; !ok {
				e.ErrorString("area_configs[%d]: default_airport %q is not an airport in this scenario", areaNum, ac.DefaultAirport)
			}
		}
	}

	for name := range util.SortedMap(s.InboundFlowDefaultRates) {
		e.Push("Inbound flow " + name)
		// Make sure the inbound flow has been defined
		if flow, ok := sg.InboundFlows[name]; !ok {
			e.ErrorString("inbound flow not found")
		} else {
			for _, ar := range flow.Arrivals {
				// Only add to VirtualControllers if the controller is local (no initial_facility).
				if ar.InitialFacility == "" {
					addController(sim.TCP(ar.InitialController))
				}
				addControllersFromWaypoints(ar.Waypoints)
				for _, rwys := range ar.RunwayWaypoints {
					for _, rwyWps := range rwys {
						addControllersFromWaypoints(rwyWps)
					}
				}
			}
			for _, of := range flow.Overflights {
				if of.InitialFacility == "" {
					addController(sim.TCP(of.InitialController))
				}
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

		}
		e.Pop()
	}

	// Remove any human-allocatable positions from VirtualControllers.
	// They may have been added from JSON or from InitialController/HandoffController in routes.
	humanPositions := s.ControllerConfiguration.AllPositions()
	s.VirtualControllers = slices.DeleteFunc(s.VirtualControllers, func(tcp sim.TCP) bool {
		return slices.Contains(humanPositions, tcp)
	})

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

	if manifest != nil {
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

func (sg *scenarioGroup) PostDeserialize(e *util.ErrorLogger, catalogs map[string]map[string]*ScenarioCatalog,
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
		e.Push("Controller " + string(position))

		if ctrl.Frequency < 118000 || ctrl.Frequency > 138000 {
			e.ErrorString("invalid frequency: %6.3f", float32(ctrl.Frequency)/1000)
		}
		if ctrl.SectorID == "" {
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
			if len(ctrl.SectorID) < 2 {
				e.ErrorString("must specify both facility and numeric sector for center controller")
			} else if !(ctrl.SectorID[0] >= 'A' && ctrl.SectorID[0] <= 'Z') {
				e.ErrorString("first character of center controller \"sector_id\" must be a letter")
			}
		}

		// Is an explicitly-given scope_char unnecessary?
		if ctrl.Scope != "" {
			if ctrl.FacilityIdentifier == ctrl.Scope {
				e.ErrorString("\"scope_char\" is redundant since it matches \"facility_id\"")
			}
			if !ctrl.ERAMFacility && ctrl.FacilityIdentifier == "" && len(ctrl.SectorID) > 0 &&
				ctrl.Scope == string(ctrl.SectorID[len(ctrl.SectorID)-1]) {
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
				sg.Airports, sg.ControlPositions, sg.FacilityAdaptation.CheckScratchpad, e)
		}
		for i := range flow.Overflights {
			flow.Overflights[i].PostDeserialize(sg, sg.NmPerLongitude, sg.MagneticVariation,
				sg.Airports, sg.ControlPositions, sg.FacilityAdaptation.CheckScratchpad, e)
		}

		// Validate cross-facility initial_controller references.
		resourcesFS := util.GetResourcesFS()
		var handoffIDs []sim.HandoffID
		if sg.HandoffTopology != nil {
			handoffIDs = sg.HandoffTopology.HandoffIDs
		}
		for i, ar := range flow.Arrivals {
			if ar.InitialFacility != "" && ar.InitialController != "" {
				e.Push("Arrival " + strconv.Itoa(i))
				validateCrossFacilityController(resourcesFS, ar.InitialFacility, ar.InitialController, handoffIDs, e)
				e.Pop()
			}
		}
		for i, of := range flow.Overflights {
			if of.InitialFacility != "" && of.InitialController != "" {
				e.Push("Overflight " + strconv.Itoa(i))
				validateCrossFacilityController(resourcesFS, of.InitialFacility, of.InitialController, handoffIDs, e)
				e.Pop()
			}
		}

		// Validate cross-facility handoff controllers in waypoints.
		for i, ar := range flow.Arrivals {
			for _, wp := range ar.Waypoints {
				if wp.HandoffControllerFacility != "" && wp.HandoffController != "" {
					e.Push("Arrival " + strconv.Itoa(i))
					validateCrossFacilityController(resourcesFS, wp.HandoffControllerFacility, wp.HandoffController, handoffIDs, e)
					e.Pop()
				}
			}
		}
		for i, of := range flow.Overflights {
			for _, wp := range of.Waypoints {
				if wp.HandoffControllerFacility != "" && wp.HandoffController != "" {
					e.Push("Overflight " + strconv.Itoa(i))
					validateCrossFacilityController(resourcesFS, wp.HandoffControllerFacility, wp.HandoffController, handoffIDs, e)
					e.Pop()
				}
			}
		}

		e.Pop()
	}

	// Validate cross-facility departure_controller references.
	{
		resourcesFS := util.GetResourcesFS()
		var hids []sim.HandoffID
		if sg.HandoffTopology != nil {
			hids = sg.HandoffTopology.HandoffIDs
		}
		for icao, ap := range sg.Airports {
			if ap.DepartureFacility != "" && ap.DepartureController != "" {
				e.Push("Airport " + icao)
				validateCrossFacilityController(resourcesFS, ap.DepartureFacility, ap.DepartureController, hids, e)
				e.Pop()
			}
			for rwy, exitroutes := range ap.DepartureRoutes {
				for exit, route := range exitroutes {
					if route.DepartureFacility != "" && route.DepartureController != "" {
						e.Push("Airport " + icao + " runway " + rwy + " exit " + exit)
						validateCrossFacilityController(resourcesFS, route.DepartureFacility, route.DepartureController, hids, e)
						e.Pop()
					}
					// Validate cross-facility handoff controllers in departure route waypoints.
					for _, wp := range route.Waypoints {
						if wp.HandoffControllerFacility != "" && wp.HandoffController != "" {
							e.Push("Airport " + icao + " runway " + rwy + " exit " + exit)
							validateCrossFacilityController(resourcesFS, wp.HandoffControllerFacility, wp.HandoffController, hids, e)
							e.Pop()
						}
					}
				}
			}
		}
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

	initializeSimConfigurations(sg, catalogs, e)
}

func (sg *scenarioGroup) rewriteControllers(e *util.ErrorLogger) {
	// Grab the original keys before rewriting.
	for position, ctrl := range sg.ControlPositions {
		ctrl.Position = string(position)
	}

	pos := make(map[sim.TCP]*av.Controller)
	for _, ctrl := range sg.ControlPositions {
		id := sim.TCP(ctrl.PositionId())
		if _, ok := pos[id]; ok {
			e.ErrorString("%s: TCP / sector_id used for multiple \"control_positions\"", id)
		}
		pos[id] = ctrl
	}

	rewriteString := func(s *string) {
		if *s == "" {
			return
		}
		if ctrl, ok := sg.ControlPositions[sim.TCP(*s)]; ok {
			*s = string(ctrl.PositionId())
		}
	}
	rewriteControlPosition := func(s *sim.ControlPosition) {
		if *s == "" {
			return
		}
		if ctrl, ok := sg.ControlPositions[*s]; ok {
			*s = sim.TCP(ctrl.PositionId())
		}
	}
	// Sector ID resolution for /ho routes.
	// Determine the local facility and host ARTCC for sector ID lookups.
	localFacility := sg.TRACON
	if localFacility == "" {
		localFacility = sg.ARTCC
	}
	isCenter := isARTCC(localFacility)

	hostARTCC := sg.ARTCC
	if hostARTCC == "" && sg.TRACON != "" {
		if info, ok := av.DB.TRACONs[sg.TRACON]; ok {
			hostARTCC = info.ARTCC
		}
	}

	// Build prefix → facility map from handoff_ids (all available lengths).
	prefixToFacility := make(map[string]string)
	if sg.HandoffTopology != nil {
		for _, hid := range sg.HandoffTopology.HandoffIDs {
			if hid.ERAMPrefix != "" {
				prefixToFacility[hid.ERAMPrefix] = hid.ID
			}
			if hid.SingleCharStarsID != "" {
				prefixToFacility[hid.SingleCharStarsID] = hid.ID
			}
			if hid.TwoCharStarsID != "" {
				prefixToFacility[hid.TwoCharStarsID] = hid.ID
			}
			if hid.StarsID != "" {
				prefixToFacility[hid.StarsID] = hid.ID
			}
			if hid.Prefix != "" {
				prefixToFacility[hid.Prefix] = hid.ID
			}
		}
	}
	// "C" always maps to the host ARTCC for TRACONs (center handoff prefix).
	if !isCenter && hostARTCC != "" {
		if _, exists := prefixToFacility["C"]; !exists {
			prefixToFacility["C"] = hostARTCC
		}
	}

	// Cache of facility → (sector_id → key_name) maps.
	sectorMaps := make(map[string]map[string]string)
	getSectorMap := func(facility string) map[string]string {
		if m, ok := sectorMaps[facility]; ok {
			return m
		}
		m := make(map[string]string)
		resourcesFS := util.GetResourcesFS()
		path := facilityConfigPathForFacility(facility)
		fc := loadFacilityConfig(resourcesFS, path, e)
		if fc != nil {
			for keyName, ctrl := range fc.ControlPositions {
				if ctrl.SectorID != "" {
					m[ctrl.SectorID] = string(keyName)
				}
			}
		}
		sectorMaps[facility] = m
		return m
	}

	// resolveSectorID resolves a sector ID from an /ho route value.
	// Returns (keyName, facility, error).
	resolveSectorID := func(s string) (string, string, error) {
		if len(s) == 0 {
			return "", "", fmt.Errorf("empty sector ID")
		}

		if len(s) <= 2 {
			// 1-2 chars: check local facility, then host ARTCC (for TRACON scenarios).
			localMap := getSectorMap(localFacility)
			if keyName, ok := localMap[s]; ok {
				return keyName, localFacility, nil
			}
			if !isCenter && hostARTCC != "" && hostARTCC != localFacility {
				artccMap := getSectorMap(hostARTCC)
				if keyName, ok := artccMap[s]; ok {
					return keyName, hostARTCC, nil
				}
			}
			return "", "", fmt.Errorf("sector %q not found in %s", s, localFacility)
		}

		// 3+ chars: try longest prefix first (3, 2, 1 chars).
		for prefixLen := min(3, len(s)-1); prefixLen >= 1; prefixLen-- {
			prefix := s[:prefixLen]
			sectorID := s[prefixLen:]

			if targetFacility, ok := prefixToFacility[prefix]; ok {
				facMap := getSectorMap(targetFacility)
				if keyName, ok := facMap[sectorID]; ok {
					return keyName, targetFacility, nil
				}
			}
		}
		return "", "", fmt.Errorf("no matching prefix/sector for %q in handoff_ids", s)
	}

	// Cache of cross-facility control positions for rewriting (moved up so rewriteWaypoints can use it).
	crossFacilityPositions := make(map[string]map[sim.TCP]*av.Controller)
	rewriteCrossFacilityPosition := func(s *sim.ControlPosition, facility string) {
		if *s == "" {
			return
		}
		positions, ok := crossFacilityPositions[facility]
		if !ok {
			resourcesFS := util.GetResourcesFS()
			fc := loadFacilityConfig(resourcesFS, facilityConfigPathForFacility(facility), e)
			if fc != nil {
				positions = fc.ControlPositions
			}
			crossFacilityPositions[facility] = positions
		}
		if positions != nil {
			if ctrl, ok := positions[*s]; ok {
				// Apply the same prefix that loadNeighborControllers used
				// so the rewritten position matches the key in sg.ControlPositions.
				var handoffIDs []sim.HandoffID
				if sg.HandoffTopology != nil {
					handoffIDs = sg.HandoffTopology.HandoffIDs
				}
				prefix := neighborPrefix(facility, handoffIDs)
				facilityId := prefix
				if isARTCC(facility) {
					facilityId = "C"
				}
				ctrlCopy := *ctrl
				ctrlCopy.SectorID = prefix + ctrlCopy.SectorID
				ctrlCopy.FacilityIdentifier = facilityId
				*s = sim.TCP(ctrlCopy.PositionId())
			}
		}
	}

	rewriteWaypoints := func(wp av.WaypointArray) {
		for i := range wp {
			if wp[i].HandoffController == "" {
				continue
			}
			hc := string(wp[i].HandoffController)

			// If the entire handoff value is just a facility prefix
			// (e.g. /hoN, /hoNN) with no sector specified, treat it
			// as a generic human handoff that needs an inbound_assignment.
			if _, isFacility := prefixToFacility[hc]; isFacility {
				wp[i].HandoffController = ""
				wp[i].HumanHandoff = true
				continue
			}

			// Resolve sector ID to full key name + facility.
			keyName, facility, err := resolveSectorID(hc)
			if err != nil {
				e.ErrorString("handoff %q: %v", hc, err)
				continue
			}
			wp[i].HandoffController = av.ControlPosition(keyName)
			if facility != localFacility {
				wp[i].HandoffControllerFacility = facility
			}

			// Rewrite to PositionId form.
			if wp[i].HandoffControllerFacility != "" {
				rewriteCrossFacilityPosition(&wp[i].HandoffController, wp[i].HandoffControllerFacility)
			} else {
				rewriteControlPosition(&wp[i].HandoffController)
			}
		}
	}

	for _, s := range sg.Scenarios {
		if len(s.Airspace) > 0 {
			a := make(map[sim.TCP][]string)
			for ctrl, vols := range s.Airspace {
				rewriteControlPosition(&ctrl)
				a[ctrl] = vols
			}
			s.Airspace = a
		}

		for _, rwy := range s.DepartureRunways {
			if ap, ok := sg.Airports[rwy.Airport]; ok {
				rewriteControlPosition(&ap.DepartureController)
			}
		}

		// Rewrite Configuration default_consolidation
		if s.ControllerConfiguration != nil {
			newPositions := make(map[sim.TCP][]sim.TCP)
			for parent, children := range s.ControllerConfiguration.DefaultConsolidation {
				rewriteControlPosition(&parent)
				newChildren := make([]sim.TCP, len(children))
				for i, child := range children {
					c := child
					rewriteControlPosition(&c)
					newChildren[i] = c
				}
				newPositions[parent] = newChildren
			}
			s.ControllerConfiguration.DefaultConsolidation = newPositions

			for flow, tcp := range s.ControllerConfiguration.InboundAssignments {
				rewriteControlPosition(&tcp)
				s.ControllerConfiguration.InboundAssignments[flow] = tcp
			}
			for airport, tcp := range s.ControllerConfiguration.DepartureAssignments {
				rewriteControlPosition(&tcp)
				s.ControllerConfiguration.DepartureAssignments[airport] = tcp
			}
		}

		for i := range s.VirtualControllers {
			rewriteControlPosition(&s.VirtualControllers[i])
		}
	}

	for _, ap := range sg.Airports {
		rewriteControlPosition(&ap.DepartureController)

		for _, exitroutes := range ap.DepartureRoutes {
			for _, route := range exitroutes {
				rewriteControlPosition(&route.HandoffController)
				rewriteControlPosition(&route.DepartureController)
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
	for position, config := range fa.ControllerConfigs {
		// Rewrite controller
		delete(fa.ControllerConfigs, position)
		p := string(position)
		rewriteString(&p)
		fa.ControllerConfigs[sim.ControlPosition(p)] = config
	}
	// Rewrite TCP references in configurations (controller assignments)
	for _, config := range fa.Configurations {
		for flow, tcp := range config.InboundAssignments {
			rewriteControlPosition(&tcp)
			config.InboundAssignments[flow] = tcp
		}
		for spec, tcp := range config.DepartureAssignments {
			rewriteControlPosition(&tcp)
			config.DepartureAssignments[spec] = tcp
		}
		// Rewrite DefaultConsolidation
		newDC := make(map[sim.TCP][]sim.TCP)
		for parent, children := range config.DefaultConsolidation {
			rewriteControlPosition(&parent)
			newChildren := make([]sim.TCP, len(children))
			for i, child := range children {
				c := child
				rewriteControlPosition(&c)
				newChildren[i] = c
			}
			newDC[parent] = newChildren
		}
		config.DefaultConsolidation = newDC
		// Rewrite FixPairAssignments
		for i := range config.FixPairAssignments {
			rewriteControlPosition(&config.FixPairAssignments[i].TCP)
		}
	}

	for _, flow := range sg.InboundFlows {
		for i := range flow.Arrivals {
			if flow.Arrivals[i].InitialFacility != "" {
				rewriteCrossFacilityPosition(&flow.Arrivals[i].InitialController, flow.Arrivals[i].InitialFacility)
			} else {
				rewriteControlPosition(&flow.Arrivals[i].InitialController)
			}
			rewriteWaypoints(flow.Arrivals[i].Waypoints)
			for _, rwyWps := range flow.Arrivals[i].RunwayWaypoints {
				for _, wps := range rwyWps {
					rewriteWaypoints(wps)
				}
			}
		}
		for i := range flow.Overflights {
			if flow.Overflights[i].InitialFacility != "" {
				rewriteCrossFacilityPosition(&flow.Overflights[i].InitialController, flow.Overflights[i].InitialFacility)
			} else {
				rewriteControlPosition(&flow.Overflights[i].InitialController)
			}
			rewriteWaypoints(flow.Overflights[i].Waypoints)
		}
	}

	// Rewrite cross-facility departure controllers.
	for _, ap := range sg.Airports {
		if ap.DepartureFacility != "" {
			rewriteCrossFacilityPosition(&ap.DepartureController, ap.DepartureFacility)
		}
		for _, exitroutes := range ap.DepartureRoutes {
			for _, route := range exitroutes {
				if route.DepartureFacility != "" {
					rewriteCrossFacilityPosition(&route.DepartureController, route.DepartureFacility)
				}
			}
		}
	}

	sg.ControlPositions = pos
}

func PostDeserializeFacilityAdaptation(s *sim.FacilityAdaptation, e *util.ErrorLogger, sg *scenarioGroup,
	manifest *sim.VideoMapManifest) {
	defer e.CheckDepth(e.CurrentDepth())

	e.Push("stars_config")

	// Validate configurations (controller assignments)
	if s.Configurations == nil {
		e.ErrorString("must provide \"configurations\"")
	}
	for configId, config := range s.Configurations {
		e.Push("configurations: " + configId)

		// Config IDs must be max 3 characters
		if len(configId) > 3 {
			e.ErrorString("configuration id %q must be at most 3 characters", configId)
		}

		// Validate that all TCPs in assignments exist in control_positions
		for flow, tcp := range config.InboundAssignments {
			if _, ok := sg.ControlPositions[tcp]; !ok {
				e.ErrorString("inbound_assignments: flow %q assigns to %q which is not in \"control_positions\"", flow, tcp)
			}
		}
		for spec, tcp := range config.DepartureAssignments {
			if _, ok := sg.ControlPositions[tcp]; !ok {
				e.ErrorString("departure_assignments: %q assigns to %q which is not in \"control_positions\"", spec, tcp)
			}
		}

		// Validate DefaultConsolidation TCPs
		for parent, children := range config.DefaultConsolidation {
			if _, ok := sg.ControlPositions[parent]; !ok {
				e.ErrorString("default_consolidation: parent %q is not in \"control_positions\"", parent)
			}
			for _, child := range children {
				if _, ok := sg.ControlPositions[child]; !ok {
					e.ErrorString("default_consolidation: child %q (under %q) is not in \"control_positions\"", child, parent)
				}
			}
		}

		// Validate fix_pair_assignments
		for _, fpa := range config.FixPairAssignments {
			if fpa.FixPairIndex < 0 || fpa.FixPairIndex >= len(sg.FixPairs) {
				e.ErrorString("fix_pair_assignments: fix_pair_index %d is out of range (have %d fix pairs)",
					fpa.FixPairIndex, len(sg.FixPairs))
			}
			if _, ok := sg.ControlPositions[fpa.TCP]; !ok {
				e.ErrorString("fix_pair_assignments: tcp %q is not in \"control_positions\"", fpa.TCP)
			}
		}

		e.Pop()
	}

	// Video maps
	for m := range s.VideoMapLabels {
		if !slices.Contains(s.VideoMapNames, m) {
			e.ErrorString("video map %q in \"map_labels\" is not in \"stars_maps\"", m)
		}
	}
	if manifest != nil {
		for _, m := range s.VideoMapNames {
			if m != "" && !manifest.HasMap(m) {
				e.ErrorString("video map %q in \"stars_maps\" is not a valid video map", m)
			}
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
			if manifest != nil {
				for _, name := range config.DefaultMaps {
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
			}

			if ctrl, ok := sg.ControlPositions[sim.TCP(tcp)]; !ok {
				e.ErrorString("Control position %q in \"controller_configs\" not defined in \"control_positions\"", tcp)
			} else if ctrl.IsExternal() {
				e.ErrorString("Control position %q in \"controller_configs\" is external and not in this TRACON.", tcp)
			}
		}
	} else if len(s.VideoMapNames) == 0 {
		e.ErrorString("Must specify either \"controller_configs\" or \"stars_maps\"")
	}

	// Validate area configs if present.
	if len(s.AreaConfigs) > 0 {
		// Collect all area numbers that controllers belong to.
		usedAreas := make(map[int]bool)
		for _, ctrl := range sg.ControlPositions {
			if ctrl.Area != 0 {
				usedAreas[ctrl.Area] = true
			}
		}
		for areaNum := range s.AreaConfigs {
			if !usedAreas[areaNum] {
				e.ErrorString("area_configs: area %d has no controllers assigned to it", areaNum)
			}
		}
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

	disp := make(map[string]any)
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

		hfr := ap.HoldForRelease
		for _, rwy := range ap.DepartureRoutes {
			for _, exitRoute := range rwy {
				if exitRoute.HoldForRelease {
					hfr = true
				}
			}
		}

		if hfr {
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
		for bl := range strings.SplitSeq(*s.MonitoredBeaconCodeBlocksString, ",") {
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

func initializeSimConfigurations(sg *scenarioGroup, catalogs map[string]map[string]*ScenarioCatalog, e *util.ErrorLogger) {
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

	catalog := &ScenarioCatalog{
		Scenarios:        make(map[string]*ScenarioSpec),
		ControlPositions: sg.ControlPositions,
		DefaultScenario:  sg.DefaultScenario,
		Facility:         facility,
		ARTCC:            artcc,
		Area:             sg.Area,
		Airports:         util.SortedMapKeys(sg.Airports),
	}

	vfrAirports := make(map[string]*av.Airport)
	for name, ap := range sg.Airports {
		if ap.VFRRateSum() > 0 {
			vfrAirports[name] = ap
		}
	}
	for name, scenario := range sg.Scenarios {
		if scenario.ControllerConfiguration == nil {
			continue
		}
		haveVFRReportingRegions := util.SeqContainsFunc(maps.Values(sg.FacilityAdaptation.ControllerConfigs),
			func(cc *sim.STARSControllerConfig) bool { return len(cc.FlightFollowingAirspace) > 0 })
		lc := sim.MakeLaunchConfig(scenario.DepartureRunways, *scenario.VFRRateScale, vfrAirports,
			scenario.InboundFlowDefaultRates, haveVFRReportingRegions)

		spec := &ScenarioSpec{
			ControllerConfiguration: scenario.ControllerConfiguration,
			LaunchConfig:            lc,
			DepartureRunways:        scenario.DepartureRunways,
			ArrivalRunways:          scenario.ArrivalRunways,
			PrimaryAirport:          sg.PrimaryAirport,
			MagneticVariation:       sg.MagneticVariation,
			WindSpecifier:           scenario.WindSpecifier,
		}

		catalog.Scenarios[name] = spec
	}

	if len(catalog.Scenarios) > 0 {
		if catalogs[facility] == nil {
			catalogs[facility] = make(map[string]*ScenarioCatalog)
		}
		catalogs[facility][sg.Name] = catalog
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

	// Check for duplicate keys in the JSON
	if dups := util.FindDuplicateJSONKeys(contents); len(dups) > 0 {
		for _, d := range dups {
			if d.Path != "" {
				e.ErrorString("duplicate JSON key %q in %s", d.Key, d.Path)
			} else {
				e.ErrorString("duplicate JSON key %q at root level", d.Key)
			}
		}
	}

	// Reject forbidden top-level keys that should now be in the facility config.
	var rawKeys map[string]json.RawMessage
	if err := json.Unmarshal(contents, &rawKeys); err == nil {
		for _, forbidden := range []string{"stars_config", "control_positions"} {
			if _, ok := rawKeys[forbidden]; ok {
				e.ErrorString("%q must not appear in scenario group files; it belongs in the facility configuration file", forbidden)
			}
		}
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

// facilityConfigPath derives the path to the facility configuration file
// from the scenario group's TRACON/ARTCC fields. The convention is:
// configurations/<ARTCC>/<facility>.json where facility is the TRACON
// (for STARS scenarios) or the ARTCC itself (for ERAM scenarios).
func facilityConfigPath(sg *scenarioGroup) string {
	facility := sg.TRACON
	if facility == "" {
		facility = sg.ARTCC
	}
	artcc := sg.ARTCC
	if artcc == "" {
		if info, ok := av.DB.TRACONs[sg.TRACON]; ok {
			artcc = info.ARTCC
		}
	}
	return "configurations/" + artcc + "/" + facility + ".json"
}

// facilityConfigPathForFacility resolves a facility name (e.g., "N90",
// "ZNY") to its configuration file path. It checks the TRACONs database
// first; if the facility is a known TRACON, it uses that TRACON's ARTCC.
// Otherwise it assumes the facility is an ARTCC itself.
// facilityConfigPathCache caches resolved facility config paths so that
// the filesystem search only happens once per facility code.
var facilityConfigPathCache = make(map[string]string)

func facilityConfigPathForFacility(facility string) string {
	if path, ok := facilityConfigPathCache[facility]; ok {
		return path
	}

	var path string
	if info, ok := av.DB.TRACONs[facility]; ok {
		path = "configurations/" + info.ARTCC + "/" + facility + ".json"
	} else if isARTCC(facility) {
		path = "configurations/" + facility + "/" + facility + ".json"
	} else {
		// Facility not in DB and not an ARTCC — search the configurations
		// directory for a matching file. This handles facilities like ATL,
		// BOS, MCO, etc. that aren't in the TRACONs database.
		resourcesFS := util.GetResourcesFS()
		filename := facility + ".json"
		_ = fs.WalkDir(resourcesFS, "configurations", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if d.Name() == filename {
				path = p
				return fs.SkipAll
			}
			return nil
		})
		if path == "" {
			// Fall back to the ARTCC-style path (will fail to load, producing an error).
			path = "configurations/" + facility + "/" + facility + ".json"
		}
	}

	facilityConfigPathCache[facility] = path
	return path
}

// validateCrossFacilityController checks that a controller referenced via
// initial_facility exists in that facility's configuration file.
func validateCrossFacilityController(filesystem fs.FS, facility string, controller av.ControlPosition,
	handoffIDs []sim.HandoffID, e *util.ErrorLogger) {
	path := facilityConfigPathForFacility(facility)
	fc := loadFacilityConfig(filesystem, path, e)
	if fc == nil {
		e.ErrorString("initial_facility %q: could not load facility config at %q", facility, path)
		return
	}
	// Check by key name first (e.g., "ZDC 05 Linden").
	if _, ok := fc.ControlPositions[sim.TCP(controller)]; ok {
		return
	}

	// The controller may already be in prefixed form (e.g., "G81" for ZAU
	// sector 81 with eram_prefix "G"). Check both raw and prefixed PositionId.
	prefix := neighborPrefix(facility, handoffIDs)
	facilityId := prefix
	if isARTCC(facility) {
		facilityId = "C"
	}
	for _, ctrl := range fc.ControlPositions {
		rawId := sim.TCP(ctrl.PositionId())
		if rawId == sim.TCP(controller) {
			return
		}
		// Build the prefixed PositionId the same way loadNeighborControllers does.
		if prefix != "" {
			ctrlCopy := *ctrl
			ctrlCopy.SectorID = prefix + ctrlCopy.SectorID
			ctrlCopy.FacilityIdentifier = facilityId
			if sim.TCP(ctrlCopy.PositionId()) == sim.TCP(controller) {
				return
			}
		}
	}
	e.ErrorString("initial_controller %q not found in facility %q (%s)", controller, facility, path)
}

// facilityConfigCache caches loaded facility configs so that multiple
// scenario groups referencing the same facility (e.g., N90) share one load.
var facilityConfigCache = make(map[string]*sim.FacilityConfig)

// loadFacilityConfig loads a facility configuration file from the given
// filesystem at the specified path. Results are cached so that shared
// facilities (like N90, referenced by jfk.json, lga.json, etc.) are only
// loaded once.
func loadFacilityConfig(filesystem fs.FS, path string, e *util.ErrorLogger) *sim.FacilityConfig {
	if fc, ok := facilityConfigCache[path]; ok {
		return fc
	}

	e.Push("Facility config " + path)
	defer e.Pop()

	contents, err := fs.ReadFile(filesystem, path)
	if err != nil {
		e.Error(err)
		return nil
	}

	if dups := util.FindDuplicateJSONKeys(contents); len(dups) > 0 {
		for _, d := range dups {
			if d.Path != "" {
				e.ErrorString("duplicate JSON key %q in %s", d.Key, d.Path)
			} else {
				e.ErrorString("duplicate JSON key %q at root level", d.Key)
			}
		}
	}

	var fc sim.FacilityConfig
	if err := util.UnmarshalJSONBytes(contents, &fc); err != nil {
		e.Error(err)
		return nil
	}

	facilityConfigCache[path] = &fc
	return &fc
}

// isARTCC returns true if the facility code looks like an ARTCC
// (starts with "Z" and is 3 characters long, e.g., "ZDC", "ZNY").
func isARTCC(facility string) bool {
	return len(facility) == 3 && strings.HasPrefix(facility, "Z")
}

// loadNeighborControllers loads controllers from a neighboring facility's
// config file and adds them to the scenario group's ControlPositions.
// The neighbor is identified by facility code (e.g., "ABE", "PHL", "ZDC").
// If the neighbor's config file doesn't exist, it's silently skipped since
// not all facilities in the real NAS have configs in this system.
//
// Each neighbor controller gets a prefix applied to its SectorID and
// FacilityIdentifier so that controllers from different facilities don't
// collide. The prefix is looked up from handoffIDs; ARTCCs not listed
// default to prefix "C". TRACONs not listed in handoffIDs are skipped.
// neighborPrefix returns the prefix string used to disambiguate a neighbor
// facility's controllers in the local ControlPositions map. Returns "" if
// the facility has no prefix (TRACON not listed in handoffIDs).
func neighborPrefix(facility string, handoffIDs []sim.HandoffID) string {
	for _, hid := range handoffIDs {
		if hid.ID == facility {
			if hid.ERAMPrefix != "" {
				return hid.ERAMPrefix
			} else if hid.SingleCharStarsID != "" {
				return hid.SingleCharStarsID
			} else if hid.TwoCharStarsID != "" {
				return hid.TwoCharStarsID
			} else if hid.StarsID != "" {
				return hid.StarsID
			} else if hid.Prefix != "" {
				return hid.Prefix
			}
			break
		}
	}
	if isARTCC(facility) {
		return "C"
	}
	return ""
}

func loadNeighborControllers(filesystem fs.FS, sg *scenarioGroup, neighbor string,
	handoffIDs []sim.HandoffID, e *util.ErrorLogger) {
	// Find the prefix for this neighbor from handoff_ids.
	// Prefer eram_prefix (NAS ID for ARTCCs), then single char, two char, full stars_id, then prefix.
	prefix := neighborPrefix(neighbor, handoffIDs)
	if prefix == "" {
		// TRACON neighbors must be in handoff_ids to be loaded.
		return
	}

	// Determine the ARTCC for this neighbor.
	var artcc string
	if isARTCC(neighbor) {
		artcc = neighbor
	} else if tracon, ok := av.DB.TRACONs[neighbor]; ok {
		artcc = tracon.ARTCC
	} else {
		// Unknown facility — skip silently since we may not have configs for all facilities.
		return
	}

	path := fmt.Sprintf("configurations/%s/%s.json", artcc, neighbor)

	// Check if file exists before trying to load.
	if _, err := fs.Stat(filesystem, path); err != nil {
		return // Neighbor config doesn't exist — not an error.
	}

	fc := loadFacilityConfig(filesystem, path, e)
	if fc == nil {
		return
	}

	// Add neighbor controllers to the scenario group's positions with
	// prefixed SectorID and FacilityIdentifier.
	// Don't overwrite existing positions (the primary facility takes precedence).
	//
	// For ARTCC neighbors, FacilityIdentifier is always "C" (universal
	// center scope char), while SectorID uses the specific ERAM prefix
	// for uniqueness. For TRACON neighbors, prefix is used for both.
	facilityId := prefix
	if isARTCC(neighbor) {
		facilityId = "C"
	}
	for _, ctrl := range fc.ControlPositions {
		ctrlCopy := deep.MustCopy(ctrl)
		ctrlCopy.SectorID = prefix + ctrlCopy.SectorID
		ctrlCopy.FacilityIdentifier = facilityId

		prefixedTCP := sim.TCP(ctrlCopy.PositionId())
		if _, exists := sg.ControlPositions[prefixedTCP]; !exists {
			sg.ControlPositions[prefixedTCP] = ctrlCopy
		}
	}
}

// LoadScenarioGroups loads all of the available scenarios, both from the
// scenarios/ directory in the source code distribution as well as,
// optionally, a scenario file provided on the command line.  It doesn't
// try to do any sort of meaningful error handling but it does try to
// continue on in the presence of errors; all errors will be printed and
// the program will exit if there are any.  We'd rather force any errors
// due to invalid scenario definitions to be fixed...
//
// If skipVideoMaps is true, video map manifests are not loaded and video map
// validation is skipped. This is useful for CLI tools that don't need video maps.
//
// Returns: scenarioGroups, catalogs, mapManifests, extraScenarioErrors
// If the extra scenario file has errors, they are returned in extraScenarioErrors
// and that scenario is not loaded, but execution continues.
func LoadScenarioGroups(extraScenarioFilename string, extraVideoMapFilename string, skipVideoMaps bool,
	e *util.ErrorLogger, lg *log.Logger) (map[string]map[string]*scenarioGroup, map[string]map[string]*ScenarioCatalog, map[string]*sim.VideoMapManifest, string) {
	start := time.Now()

	// First load the scenarios.
	scenarioGroups := make(map[string]map[string]*scenarioGroup)
	catalogs := make(map[string]map[string]*ScenarioCatalog)

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

			// Load facility config for the extra scenario.
			extraResourcesFS := util.GetResourcesFS()
			fc := loadFacilityConfig(extraResourcesFS, facilityConfigPath(s), &extraE)
			if fc != nil {
				s.ControlPositions = deep.MustCopy(fc.ControlPositions)
				s.FacilityAdaptation = deep.MustCopy(fc.FacilityAdaptation)
				s.HandoffTopology = fc.HandoffTopology
				s.FixPairs = fc.FixPairs

				if fc.HandoffTopology != nil {
					for _, neighbor := range fc.HandoffTopology.NeighboringFacilities {
						loadNeighborControllers(extraResourcesFS, s, neighbor, fc.HandoffTopology.HandoffIDs, &extraE)
					}
				}
			}

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

	// Load video map manifests if needed for validation.
	mapManifests := make(map[string]*sim.VideoMapManifest)
	if !skipVideoMaps {
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

		// Load the video map specified on the command line, if any.
		if extraVideoMapFilename != "" {
			mapManifests[extraVideoMapFilename], err = sim.LoadVideoMapManifest(extraVideoMapFilename)
			if err != nil {
				lg.Errorf("%s: %v", extraVideoMapFilename, err)
				os.Exit(1)
			}
		}
	}

	lg.Infof("scenario/video map manifest load time: %s\n", time.Since(start))

	// Load facility configs and attach to scenario groups. This must happen
	// before PostDeserialize since it populates ControlPositions and
	// FacilityAdaptation which are needed for validation.
	resourcesFS := util.GetResourcesFS()
	for _, tracon := range scenarioGroups {
		for _, sg := range tracon {
			fc := loadFacilityConfig(resourcesFS, facilityConfigPath(sg), e)
			if fc == nil {
				continue
			}

			sg.ControlPositions = deep.MustCopy(fc.ControlPositions)
			sg.FacilityAdaptation = deep.MustCopy(fc.FacilityAdaptation)
			sg.HandoffTopology = fc.HandoffTopology
			sg.FixPairs = fc.FixPairs

			// Add missing airports referenced by altimeters and coordination
			// lists from sibling scenario groups. The facility config is
			// shared across all scenario groups for a TRACON, but sub-area
			// scenarios only define a subset of airports.
			addFromSibling := func(airport string) {
				if _, ok := sg.Airports[airport]; ok {
					return
				}
				for _, sibling := range tracon {
					if sibling == sg {
						continue
					}
					if _, ok := sibling.Airports[airport]; ok {
						if sg.Airports == nil {
							sg.Airports = make(map[string]*av.Airport)
						}
						sg.Airports[airport] = &av.Airport{}
						return
					}
				}
			}
			for _, ap := range sg.FacilityAdaptation.Altimeters {
				addFromSibling(ap)
			}
			for _, cl := range sg.FacilityAdaptation.CoordinationLists {
				for _, ap := range cl.Airports {
					addFromSibling(ap)
					// Airports in coordination lists must be hold for release.
					if a, ok := sg.Airports[ap]; ok && !a.HoldForRelease {
						a.HoldForRelease = true
					}
				}
			}

			// Load controllers from neighboring facilities.
			if fc.HandoffTopology != nil {
				for _, neighbor := range fc.HandoffTopology.NeighboringFacilities {
					loadNeighborControllers(resourcesFS, sg, neighbor, fc.HandoffTopology.HandoffIDs, e)
				}
			}
		}
	}
	if e.HaveErrors() {
		return nil, nil, nil, ""
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

			if skipVideoMaps {
				// When skipping video maps, still call PostDeserialize but with nil manifest
				// to initialize catalogs and set default values
				sgroup.PostDeserialize(e, catalogs, nil)
			} else {
				// Make sure we have what we need in terms of video maps
				fa := &sgroup.FacilityAdaptation
				if vf := fa.VideoMapFile; vf == "" {
					e.ErrorString("no \"video_map_file\" specified")
				} else if manifest, ok := mapManifests[vf]; !ok {
					e.ErrorString("no manifest for video map %q found. Options: %s", vf,
						strings.Join(util.SortedMapKeys(mapManifests), ", "))
				} else {
					sgroup.PostDeserialize(e, catalogs, manifest)
				}
			}

			e.Pop()
		}
		e.Pop()
	}

	// Validate the extra scenario separately with its own error logger
	if extraScenario != nil {
		if skipVideoMaps {
			// When skipping video maps, still call PostDeserialize but with nil manifest
			var extraE util.ErrorLogger
			extraScenario.PostDeserialize(&extraE, catalogs, nil)
			if scenarioGroups[extraScenarioFacility] == nil {
				scenarioGroups[extraScenarioFacility] = make(map[string]*scenarioGroup)
			}
			scenarioGroups[extraScenarioFacility][extraScenario.Name] = extraScenario
		} else {
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
				extraScenario.PostDeserialize(&extraE, catalogs, manifest)
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

	return scenarioGroups, catalogs, mapManifests, extraScenarioErrors
}

// ListAllScenarios returns a sorted list of all available scenarios in TRACON/scenario format
func ListAllScenarios(scenarioFilename, videoMapFilename string, lg *log.Logger) ([]string, error) {
	var e util.ErrorLogger
	scenarioGroups, _, _, _ := LoadScenarioGroups(scenarioFilename, videoMapFilename, true /* skipVideoMaps */, &e, lg)
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
func LookupScenario(tracon, scenarioName string, scenarioGroups map[string]map[string]*scenarioGroup, catalogs map[string]map[string]*ScenarioCatalog) (*ScenarioCatalog, *scenarioGroup, error) {
	if groups, ok := scenarioGroups[tracon]; ok {
		for _, group := range groups {
			if _, ok := group.Scenarios[scenarioName]; ok {
				if facilityCatalogs, ok := catalogs[tracon]; ok {
					for _, catalog := range facilityCatalogs {
						if catalog.Scenarios[scenarioName] != nil {
							return catalog, group, nil
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
	haveVFRReportingRegions := util.SeqContainsFunc(maps.Values(scenarioGroup.FacilityAdaptation.ControllerConfigs),
		func(cfg *sim.STARSControllerConfig) bool { return cfg.FlightFollowingAirspace != nil })

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
func CreateNewSimConfiguration(catalog *ScenarioCatalog, scenarioGroup *scenarioGroup, scenarioName string) (*sim.NewSimConfiguration, error) {
	scenario, ok := scenarioGroup.Scenarios[scenarioName]
	if !ok {
		return nil, fmt.Errorf("scenario %s not found in group", scenarioName)
	}

	simConfig := catalog.Scenarios[scenarioName]
	if simConfig == nil {
		return nil, fmt.Errorf("scenario configuration %s not found", scenarioName)
	}

	newSimConfig := &sim.NewSimConfiguration{
		Facility:                scenarioGroup.TRACON,
		Description:             scenarioName,
		LaunchConfig:            CreateLaunchConfig(scenario, scenarioGroup),
		DepartureRunways:        simConfig.DepartureRunways,
		ArrivalRunways:          simConfig.ArrivalRunways,
		PrimaryAirport:          simConfig.PrimaryAirport,
		Airports:                scenarioGroup.Airports,
		Fixes:                   scenarioGroup.Fixes,
		VFRReportingPoints:      scenarioGroup.VFRReportingPoints,
		ControlPositions:        scenarioGroup.ControlPositions,
		ControllerConfiguration: scenario.ControllerConfiguration,
		InboundFlows:            scenarioGroup.InboundFlows,
		FacilityAdaptation:      deep.MustCopy(scenarioGroup.FacilityAdaptation),
		ReportingPoints:         scenarioGroup.ReportingPoints,
		MagneticVariation:       scenarioGroup.MagneticVariation,
		NmPerLongitude:          scenarioGroup.NmPerLongitude,
		WindSpecifier:           scenario.WindSpecifier,
		Center:                  util.Select(scenario.Center.IsZero(), scenarioGroup.FacilityAdaptation.Center, scenario.Center),
		Range:                   util.Select(scenario.Range == 0, scenarioGroup.FacilityAdaptation.Range, scenario.Range),
		DefaultMaps:             scenario.DefaultMaps,
		DefaultMapGroup:         scenario.DefaultMapGroup,
		Airspace:                scenarioGroup.Airspace,
		ControllerAirspace:      scenario.Airspace,
		VirtualControllers:      scenario.VirtualControllers,
		HandoffTopology:         scenarioGroup.HandoffTopology,
		FixPairs:                scenarioGroup.FixPairs,
	}

	// Resolve fix pair assignments from the selected configuration
	if scenario.ControllerConfiguration != nil && scenario.ControllerConfiguration.ConfigId != "" {
		if config, ok := scenarioGroup.FacilityAdaptation.Configurations[scenario.ControllerConfiguration.ConfigId]; ok {
			newSimConfig.FixPairAssignments = config.FixPairAssignments
		}
	}

	return newSimConfig, nil
}

// scenario/catalog.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scenario

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/traffic"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/videomaps"
	"github.com/mmp/vice/wx"
)

// BriefRegistry holds the per-facility brief metadata gathered at
// scenario load time and consulted at sim-creation time.
type BriefRegistry struct {
	facilities     map[string]struct{}          // which facilities have briefs
	videoMapHashes map[string]map[string][]byte // facility -> video map filenames -> hashes
	pathOverrides  map[string]string            // non-canonical brief paths (set only by --scenario-brief)
}

func newBriefRegistry() *BriefRegistry {
	return &BriefRegistry{
		facilities:     make(map[string]struct{}),
		videoMapHashes: make(map[string]map[string][]byte),
		pathOverrides:  make(map[string]string),
	}
}

func (r *BriefRegistry) register(facility string, hashes map[string][]byte, pathOverride string) {
	r.facilities[facility] = struct{}{}
	if len(hashes) > 0 {
		r.videoMapHashes[facility] = hashes
	}
	if pathOverride != "" {
		r.pathOverrides[facility] = pathOverride
	}
}

// VideoMapHashes gives the hashes of the video maps the facility's brief
// refers to, so that clients can check they have the same ones.
func (r *BriefRegistry) VideoMapHashes(facility string) map[string][]byte {
	return r.videoMapHashes[facility]
}

// LoadBrief returns the markdown source for the given facility's brief,
// or ("", nil) if no brief was registered for it at startup. The source
// is the --scenario-brief override when present, otherwise the canonical
// briefs/<ARTCC>/<facility>.md resource. Reads only init-immutable maps,
// so needs no locking.
func (r *BriefRegistry) LoadBrief(facility string) (string, error) {
	if _, ok := r.facilities[facility]; !ok {
		return "", nil
	}
	if p, ok := r.pathOverrides[facility]; ok {
		b, err := os.ReadFile(p)
		return string(b), err
	}
	path := scenarioBriefPath(facility)
	if !util.ResourceExists(path) {
		return "", fmt.Errorf("brief resource %q not found", path)
	}
	return string(util.LoadResourceBytes(path)), nil
}

// Tables is the result of loading and validating the scenario and
// facility configuration files.
type Tables struct {
	Groups      map[string]map[string]*Group
	Catalogs    map[string]map[string]*Catalog
	MapSpecs    map[string]*videomaps.LibrarySpec
	Briefs      *BriefRegistry
	Emergencies []sim.Emergency
}

func makeTables(groups map[string]map[string]*Group, catalogs map[string]map[string]*Catalog,
	mapSpecs map[string]*videomaps.LibrarySpec, briefs *BriefRegistry) *Tables {
	return &Tables{
		Groups:   groups,
		Catalogs: catalogs,
		MapSpecs: mapSpecs,
		Briefs:   briefs,
		// Load already validated emergencies.json; re-parse to hand the list to sim sessions.
		Emergencies: loadEmergencies(nil),
	}
}

// Client-side info about the available scenarios.
type Catalog struct {
	Scenarios        map[string]*Spec
	ControlPositions map[sim.TCP]*av.Controller
	DefaultScenario  string
	Facility         string
	ARTCC            string
	Area             string
	Airports         []av.ICAOAirportCode // airports in this scenario group
}

type Spec struct {
	ControllerConfiguration *sim.ControllerConfiguration
	MagneticVariation       float32
	WindSpecifier           *wx.WindSpecifier
	Timetables              []traffic.TimetableSummary
	// TrafficSources are the sources this scenario can be flown with, in the
	// order they should be offered. A scenario that gives no airlines can't
	// generate its own traffic, so it offers only the published sources.
	TrafficSources []sim.TrafficSource
	// HistoricalFlightIntervals are the stretches of time the historical flight
	// data covers; it has a gap wherever the list does.
	HistoricalFlightIntervals []util.TimeInterval

	LaunchConfig sim.LaunchConfig

	Description      string
	DepartureRunways []sim.DepartureRunway
	ArrivalRunways   []sim.ArrivalRunway
	Center           math.Point2LL
}

func (s *Spec) AllAirports() []av.ICAOAirportCode {
	allAirports := make(map[av.ICAOAirportCode]bool)
	for _, runway := range s.DepartureRunways {
		allAirports[runway.Airport] = true
	}
	for _, runway := range s.ArrivalRunways {
		allAirports[runway.Airport] = true
	}
	return util.SortedMapKeys(allAirports)
}

func initializeSimConfigurations(sg *Group, catalogs map[string]map[string]*Catalog, e *util.ErrorLogger) {
	facility := sg.facility()
	artcc := sg.ARTCC
	if artcc == "" {
		artcc = db.DB.ARTCCForFacility(facility)
	}

	catalog := &Catalog{
		Scenarios:        make(map[string]*Spec),
		ControlPositions: sg.FacilityConfig.ControlPositions,
		DefaultScenario:  sg.DefaultScenario,
		Facility:         facility,
		ARTCC:            artcc,
		Area:             sg.Area,
		Airports:         util.SortedMapKeys(sg.Airports),
	}

	vfrAirports := make(map[av.ICAOAirportCode]*av.Airport)
	for name, ap := range sg.Airports {
		if ap.VFRRateSum() > 0 {
			vfrAirports[name] = ap
		}
	}
	for name, scenario := range sg.Scenarios {
		if scenario.ConfigurationString == "" {
			continue
		}
		haveVFRReportingRegions := util.SeqContainsFunc(maps.Values(sg.FacilityConfig.FacilityAdaptation.Controllers),
			func(cc *sim.STARSController) bool { return len(cc.FlightFollowingAirspace) > 0 })
		lc := sim.MakeLaunchConfig(scenario.DepartureRunways, *scenario.VFRRateScale, *scenario.VFFRequestRate,
			vfrAirports, scenario.InboundFlowDefaultRates, haveVFRReportingRegions)
		sim.MarkBackgroundTraffic(sg.Airports, sg.InboundFlows, &scenario.ControllerConfiguration,
			sg.FacilityConfig.ControlPositions, &lc)

		spec := &Spec{
			ControllerConfiguration: &scenario.ControllerConfiguration,
			LaunchConfig:            lc,
			Description:             scenario.Description,
			DepartureRunways:        scenario.DepartureRunways,
			ArrivalRunways:          scenario.ArrivalRunways,
			MagneticVariation:       sg.MagneticVariation,
			WindSpecifier:           scenario.WindSpecifier,
			Center:                  scenario.center(sg),
		}
		if canGenerateScenarioTraffic(sg, &lc) {
			spec.TrafficSources = append(spec.TrafficSources, sim.TrafficSourceScenario)
		}

		catalog.Scenarios[name] = spec
	}

	if len(catalog.Scenarios) > 0 {
		if catalogs[facility] == nil {
			catalogs[facility] = make(map[string]*Catalog)
		}
		catalogs[facility][sg.Name] = catalog
	}
}

// canGenerateScenarioTraffic reports whether the scenario has the airline lists
// its own traffic generator samples from. They are optional; without them the
// scenario can only be flown from a timetable or historical data, which bring
// their own callsigns and aircraft types.
func canGenerateScenarioTraffic(sg *Group, lc *sim.LaunchConfig) bool {
	for airport := range lc.DepartureRates {
		ap, ok := sg.Airports[airport]
		if !ok {
			return false
		}
		if !slices.ContainsFunc(ap.Departures,
			func(dep av.Departure) bool { return len(dep.Airlines) > 0 }) {
			return false
		}
	}

	for flow, airports := range lc.InboundFlowRates {
		inboundFlow, ok := sg.InboundFlows[flow]
		if !ok {
			return false
		}
		for airport := range airports {
			if airport == "overflights" {
				continue // overflights always come from the scenario's own airlines
			}
			if !slices.ContainsFunc(inboundFlow.Arrivals,
				func(arr av.Arrival) bool { return len(arr.Airlines[av.ICAOAirportCode(airport)]) > 0 }) {
				return false
			}
		}
	}

	return true
}

func attachTimetables(catalogs map[string]map[string]*Catalog, timetables traffic.TimetableCatalog) {
	for _, facilityCatalogs := range catalogs {
		for _, catalog := range facilityCatalogs {
			for _, scenario := range catalog.Scenarios {
				for _, airport := range scenario.AllAirports() {
					scenario.Timetables = append(scenario.Timetables,
						timetables.SummariesForAirport(airport)...)
				}
				if len(scenario.Timetables) > 0 {
					scenario.TrafficSources = append(scenario.TrafficSources, sim.TrafficSourceTimetable)
				}
			}
		}
	}
}

// attachHistoricalFlightIntervals records the stretches of time the historical
// flight data covers, so that the client can offer start times the data
// actually covers. The stretches are the same everywhere--the source goes down
// for all of it at once--so what decides whether a scenario can be flown from
// it is whether the cells its airports are in hold anything.
func attachHistoricalFlightIntervals(catalogs map[string]map[string]*Catalog, lg *log.Logger) {
	resources := util.GetResourcesFS()
	intervals, err := traffic.FlightDataIntervals(resources)
	if err != nil {
		lg.Errorf("historical flight data: %v", err)
		return
	}
	if len(intervals) == 0 {
		return
	}

	for _, facilityCatalogs := range catalogs {
		for _, catalog := range facilityCatalogs {
			for _, scenario := range catalog.Scenarios {
				if !haveFlightDataCells(scenario) {
					continue
				}
				scenario.HistoricalFlightIntervals = intervals
				scenario.TrafficSources = append(scenario.TrafficSources, sim.TrafficSourceHistorical)
			}
		}
	}
}

// haveFlightDataCells reports whether any of the cells covering a scenario's
// airports has flight data at all.
func haveFlightDataCells(scenario *Spec) bool {
	departures, arrivals := scenario.LaunchConfig.IFRAirports()
	return slices.ContainsFunc(traffic.FlightDataCells(departures, arrivals), func(cell string) bool {
		return util.ResourceExists(traffic.FlightDataPath(cell))
	})
}

// checkInboundAssignments reports an "inbound_assignments" entry in a facility
// config that names no inbound flow. An assignment a single scenario doesn't
// use is fine--the config covers every flow in the facility--but one that no
// scenario group of the facility defines at all is left over from a renamed
// flow and quietly assigns nothing.
func checkInboundAssignments(scenarioGroups map[string]map[string]*Group, e *util.ErrorLogger) {
	for facility, groups := range util.SortedMap(scenarioGroups) {
		flows := make(map[string]struct{})
		for _, sg := range groups {
			for name := range sg.InboundFlows {
				flows[name] = struct{}{}
			}
		}

		// The groups of a facility carry copies of the same config, so
		// gather the dead names before reporting any of them.
		dead := make(map[string]map[string]struct{}) // configuration id -> flow names
		for _, sg := range groups {
			for id, config := range sg.FacilityConfig.FacilityAdaptation.Configurations {
				for flow := range config.InboundAssignments {
					if _, ok := flows[flow]; ok {
						continue
					}
					if dead[id] == nil {
						dead[id] = make(map[string]struct{})
					}
					dead[id][flow] = struct{}{}
				}
			}
		}

		for _, id := range util.SortedMapKeys(dead) {
			for _, flow := range util.SortedMapKeys(dead[id]) {
				e.ErrorString(`%s: configurations: %s: inbound_assignments: %q names no inbound flow`,
					facility, id, flow)
			}
		}
	}
}

// finalizeTrafficSources settles which source each scenario starts on, once
// every source has had its say. A scenario with nothing at all to fly is a
// scenario file that needs fixing.
func finalizeTrafficSources(catalogs map[string]map[string]*Catalog,
	scenarioGroups map[string]map[string]*Group, e *util.ErrorLogger) {
	for facility, facilityCatalogs := range catalogs {
		for name, catalog := range facilityCatalogs {
			for scenarioName, scenario := range catalog.Scenarios {
				if len(scenario.TrafficSources) == 0 {
					e.ErrorString("%s/%s/%s: no traffic source can fly this scenario: it gives no "+
						"airlines and the facility has neither historical flight data nor a timetable",
						facility, name, scenarioName)
					continue
				}
				slices.Sort(scenario.TrafficSources)
				if !slices.Contains(scenario.TrafficSources, scenario.LaunchConfig.TrafficSource) {
					scenario.LaunchConfig.TrafficSource = scenario.TrafficSources[0]
				}

				// A scenario that can only be flown from published traffic has
				// nothing to fly at an airport none of its arrivals takes STAR
				// traffic into. How much of an airport's traffic is placed is
				// not something a load-time check can see--it depends on what
				// really flew--so this only catches losing all of it.
				if slices.Contains(scenario.TrafficSources, sim.TrafficSourceScenario) {
					continue
				}
				sg, ok := scenarioGroups[facility][name]
				if !ok {
					continue
				}
				for _, airport := range airportsWithoutSTARArrivals(sg, &scenario.LaunchConfig) {
					e.ErrorString("%s/%s/%s: no arrival into %s takes any STAR's traffic, so "+
						`published traffic there is all dropped; give them "star_feeds"`,
						facility, name, scenarioName, airport)
				}
			}
		}
	}
}

// airportsWithoutSTARArrivals returns the airports the scenario lands traffic
// at that no active arrival takes STAR traffic into, in sorted order. Those
// lose every flight that files a STAR, which is all of the airline traffic.
func airportsWithoutSTARArrivals(sg *Group, lc *sim.LaunchConfig) []string {
	served := make(map[string]bool)
	for flow, airports := range lc.InboundFlowRates {
		inboundFlow, ok := sg.InboundFlows[flow]
		if !ok {
			continue
		}
		for airport := range airports {
			if airport == "overflights" || served[airport] {
				continue
			}
			served[airport] = slices.ContainsFunc(inboundFlow.Arrivals, func(arr av.Arrival) bool {
				return slices.Contains(arr.Airports, av.ICAOAirportCode(airport)) && len(arr.ServedSTARs()) > 0
			})
		}
	}
	return util.FilterSlice(util.SortedMapKeys(served), func(airport string) bool { return !served[airport] })
}

// loadEmergencies loads and validates the emergencies.json resource file.
// Errors are reported via the ErrorLogger.
func loadEmergencies(e *util.ErrorLogger) []sim.Emergency {
	e.Push("File emergencies.json")
	defer e.Pop()

	r := util.LoadResource("emergencies.json")
	defer r.Close()

	var emap map[string][]sim.Emergency // "emergencies": [ ... ]
	if err := util.UnmarshalJSON(r, &emap); err != nil {
		e.Error(err)
		return nil
	}
	emergencies := emap["emergencies"]

	if len(emergencies) == 0 {
		e.ErrorString(`No "emergencies" found`)
		return nil
	}

	namesSeen := make(map[string]struct{})
	for i := range emergencies {
		em := &emergencies[i] // so we can modify it...

		if _, ok := namesSeen[em.Name]; ok {
			e.ErrorString("Duplicate emergency name %q", em.Name)
			continue
		}
		namesSeen[em.Name] = struct{}{}

		e.Push(em.Name)

		// Default weight to 1.0 if not specified
		if em.Weight == 0 {
			em.Weight = 1
		}

		if em.ApplicableToString == "" {
			e.ErrorString("missing required field 'applicable_to'")
		} else {
			for typeStr := range strings.SplitSeq(em.ApplicableToString, ",") {
				typeStr = strings.TrimSpace(typeStr)
				switch typeStr {
				case "departure":
					em.ApplicableTo |= sim.EmergencyApplicabilityDeparture
				case "arrival":
					em.ApplicableTo |= sim.EmergencyApplicabilityArrival
				case "external":
					em.ApplicableTo |= sim.EmergencyApplicabilityExternal
				case "approach":
					em.ApplicableTo |= sim.EmergencyApplicabilityApproach
				default:
					e.ErrorString(`invalid "applicable_to" value %q: must be one or more of "departure", "arrival", "external", "approach" (comma-separated)`,
						typeStr)
				}
			}
		}

		if len(em.Stages) == 0 {
			e.ErrorString(`no emergency "stages" defined`)
		}
		for i, stage := range em.Stages {
			// transmission is required unless request_return is true
			if stage.Transmission == "" && !stage.RequestReturn {
				e.ErrorString(`stage %d missing required field "transmission"`, i)
			}
			// duration_minutes is required for all stages except the last one
			isLastStage := i == len(em.Stages)-1
			if !isLastStage {
				if stage.DurationMinutes[1] == 0 {
					e.ErrorString(`stage %d missing required field "duration_minutes"`, i)
				}
				if stage.DurationMinutes[0] > stage.DurationMinutes[1] {
					e.ErrorString(`First value in "duration_minutes" cannot be greater than second`)
				}
			}
		}
		e.Pop()
	}

	return emergencies
}

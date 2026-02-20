// simconfig.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"errors"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/AllenDang/cimgui-go/imgui"
)

type NewSimConfiguration struct {
	server.NewSimRequest

	selectedFacilityCatalogs map[string]*server.ScenarioCatalog

	displayError error

	mgr             *client.ConnectionManager
	selectedServer  *client.Server
	defaultFacility *string
	tfrCache        *av.TFRCache
	emergencies     []sim.Emergency
	lg              *log.Logger

	// UI state
	newSimType          newSimType
	joinRequest         server.JoinSimRequest
	showAllMETAR        bool
	showReliefPositions bool
	selectedTCW         sim.TCW
	selectedTCPs        map[sim.TCP]bool

	// New UI state for improved flow
	filterText string // search/filter for scenario selection

	// Weather filter UI state
	weatherFilter      wx.WeatherFilter
	weatherFilterError string

	mu              util.LoggingMutex // protects airportMETAR/availableWXIntervals
	airportMETAR    map[string][]wx.METAR
	metarAirports   []string
	fetchMETARError error

	// Winds aloft data for the current facility
	atmosByTime        *wx.AtmosByTime
	windsAloftAltitude float32

	availableWXIntervals []util.TimeInterval

	savedVFRDepartureRateScale float32
}

// loadEmergencies loads all emergency types from the emergencies.json resource file.
// Any errors encountered during loading are reported via the ErrorLogger.
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
		e.ErrorString("No \"emergencies\" found")
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
					e.ErrorString("invalid \"applicable_to\" value %q: must be one or more of \"departure\", \"arrival\", \"external\", \"approach\" (comma-separated)",
						typeStr)
				}
			}
		}

		if len(em.Stages) == 0 {
			e.ErrorString("no emergency \"stages\" defined")
		}
		for i, stage := range em.Stages {
			// transmission is required unless request_return is true
			if stage.Transmission == "" && !stage.RequestReturn {
				e.ErrorString("stage %d missing required field \"transmission\"", i)
			}
			// duration_minutes is required for all stages except the last one
			isLastStage := i == len(em.Stages)-1
			if !isLastStage {
				if stage.DurationMinutes[1] == 0 {
					e.ErrorString("stage %d missing required field \"duration_minutes\"", i)
				}
				if stage.DurationMinutes[0] > stage.DurationMinutes[1] {
					e.ErrorString("First value in \"duration_minutes\" cannot be greater than second")
				}
			}
		}
		e.Pop()
	}

	return emergencies
}

func MakeNewSimConfiguration(mgr *client.ConnectionManager, defaultFacility *string, tfrCache *av.TFRCache, lg *log.Logger) *NewSimConfiguration {
	var emergencyLogger util.ErrorLogger
	emergencies := loadEmergencies(&emergencyLogger)
	if emergencyLogger.HaveErrors() {
		emergencyLogger.PrintErrors(lg)
	}

	c := &NewSimConfiguration{
		lg:              lg,
		mgr:             mgr,
		selectedServer:  mgr.LocalServer,
		defaultFacility: defaultFacility,
		tfrCache:        tfrCache,
		emergencies:     emergencies,
		NewSimRequest:   server.MakeNewSimRequest(),
	}

	c.SetFacility(*defaultFacility)

	return c
}

func (c *NewSimConfiguration) SetFacility(name string) {
	var ok bool
	catalogs := c.selectedServer.GetScenarioCatalogs()
	if c.selectedFacilityCatalogs, ok = catalogs[name]; !ok {
		if name != "" {
			c.lg.Errorf("%s: TRACON not found!", name)
		}
		// Pick one at random
		name = util.SortedMapKeys(catalogs)[rand.Make().Intn(len(catalogs))]
		c.selectedFacilityCatalogs = catalogs[name]
	}
	c.Facility = name
	var scenarioCatalog *server.ScenarioCatalog
	c.GroupName, scenarioCatalog = util.FirstSortedMapEntry(c.selectedFacilityCatalogs)

	c.SetScenario(c.GroupName, scenarioCatalog.DefaultScenario)
}

func (c *NewSimConfiguration) SetScenario(groupName, scenarioName string) {
	var ok bool
	var scenarioCatalog *server.ScenarioCatalog
	if scenarioCatalog, ok = c.selectedFacilityCatalogs[groupName]; !ok {
		c.lg.Errorf("%s: group not found in TRACON %s", groupName, c.Facility)
		groupName, scenarioCatalog = util.FirstSortedMapEntry(c.selectedFacilityCatalogs)
	}
	c.GroupName = groupName

	if c.ScenarioSpec, ok = scenarioCatalog.Scenarios[scenarioName]; !ok {
		if scenarioName != "" {
			c.lg.Errorf("%s: scenario not found in group %s", scenarioName, c.GroupName)
		}
		scenarioName = scenarioCatalog.DefaultScenario
		c.ScenarioSpec = scenarioCatalog.Scenarios[scenarioName]
	}
	c.ScenarioName = scenarioName
	c.savedVFRDepartureRateScale = c.ScenarioSpec.LaunchConfig.VFRDepartureRateScale

	// Initialize default wind direction from runways
	c.initDefaultWindDirection()

	go c.fetchMETAR()
}

// initDefaultWindDirection computes the default wind direction range from the scenario's runways.
// It calculates the average runway heading and sets a ±30 degree range around it.
func (c *NewSimConfiguration) initDefaultWindDirection() {
	if c.ScenarioSpec == nil {
		return
	}

	// Calculate average runway heading
	var sumRunwayVecs [2]float32
	ap := c.ScenarioSpec.PrimaryAirport
	if dbap, ok := av.DB.Airports[ap]; ok {
		for _, rwy := range dbap.Runways {
			if slices.ContainsFunc(c.ScenarioSpec.DepartureRunways, func(r sim.DepartureRunway) bool {
				return r.Airport == ap && r.Runway.Base() == rwy.Id
			}) {
				sumRunwayVecs = math.Add2f(sumRunwayVecs, math.HeadingVector(rwy.Heading))
			}
			if slices.ContainsFunc(c.ScenarioSpec.ArrivalRunways, func(r sim.ArrivalRunway) bool {
				return r.Airport == ap && r.Runway.Base() == rwy.Id
			}) {
				sumRunwayVecs = math.Add2f(sumRunwayVecs, math.HeadingVector(rwy.Heading))
			}
		}
	}

	avgRwyHeading := math.VectorHeading(sumRunwayVecs)
	avgRwyMagneticHeading := avgRwyHeading + c.ScenarioSpec.MagneticVariation

	// Set default wind direction range to ±30 degrees from average runway heading
	windDirMin := int(math.NormalizeHeading(avgRwyMagneticHeading - 30))
	windDirMax := int(math.NormalizeHeading(avgRwyMagneticHeading + 30))

	// Reset weather filter for the new scenario with default wind direction
	c.weatherFilter = wx.WeatherFilter{
		WindDirMin: &windDirMin,
		WindDirMax: &windDirMax,
	}
	c.weatherFilterError = ""
}

func (c *NewSimConfiguration) fetchMETAR() {
	c.mu.Lock(c.lg)
	defer c.mu.Unlock(c.lg)

	if c.ScenarioSpec == nil {
		return
	}

	airports := c.ScenarioSpec.AllAirports()
	if slices.Equal(c.metarAirports, airports) {
		// No need to refetch, but the scenario may have changed
		// (different runways / weather filter), so resample the start time.
		c.updateStartTimeForRunways()
		return
	}

	c.airportMETAR = nil
	c.fetchMETARError = nil
	c.availableWXIntervals = nil
	c.atmosByTime = nil

	// Load METAR data from local resources
	metarSOA, err := wx.GetMETAR(airports)
	if err != nil {
		c.fetchMETARError = err
		return
	}

	// Decode SOA to regular METAR slices
	metars := make(map[string][]wx.METAR)
	for ap, soa := range metarSOA {
		metars[ap] = soa.Decode()
	}

	c.airportMETAR = metars
	c.metarAirports = airports

	c.loadAtmosphericData()
	c.computeAvailableWXIntervals()
	c.updateStartTimeForRunways()
}

// loadAtmosphericData loads atmospheric data for the current facility and determines
// the appropriate winds aloft altitude based on whether it's a TRACON or ARTCC.
func (c *NewSimConfiguration) loadAtmosphericData() {
	// Determine if this is a TRACON or ARTCC based on the facility
	_, isTRACON := av.DB.TRACONs[c.Facility]

	// Set altitude based on facility type:
	// - TRACON: 5,000' - representative of terminal area traffic
	// - Center/ERAM (ARTCC): FL280 (28,000') - representative of en route traffic
	if isTRACON {
		c.windsAloftAltitude = 5000
	} else {
		c.windsAloftAltitude = 28000
	}

	// Try to load atmospheric data for the facility
	atmosByTime, err := wx.GetAtmosByTime(c.Facility)
	if err != nil {
		// We don't yet have data for center scenarios; don't treat this as an error.
		c.atmosByTime = nil
		return
	}

	c.atmosByTime = atmosByTime
}

func (c *NewSimConfiguration) computeAvailableWXIntervals() {
	// Extract METAR times from all airports
	var metarTimes []time.Time
	for _, metars := range c.airportMETAR {
		for _, m := range metars {
			metarTimes = append(metarTimes, m.Time.UTC())
		}
	}
	slices.SortFunc(metarTimes, func(a, b time.Time) int { return a.Compare(b) })

	// Compute METAR intervals
	var metarIntervals []util.TimeInterval
	if len(metarTimes) > 0 {
		metarIntervals = wx.METARIntervals(metarTimes)
	}

	// Get TRACON intervals from local resources
	traconIntervalsMap := wx.GetTimeIntervals()
	var traconIntervals []util.TimeInterval
	if intervals, ok := traconIntervalsMap[c.Facility]; ok {
		traconIntervals = intervals
	}

	if len(traconIntervals) == 0 {
		// Just use the METAR.
		c.availableWXIntervals = wx.MergeAndAlignToMidnight(metarIntervals)
	} else {
		// Intersect METAR intervals with TRACON intervals and align to midnight
		c.availableWXIntervals = wx.MergeAndAlignToMidnight(metarIntervals, traconIntervals)
	}
}

const (
	NewSimCreateLocal = iota
	NewSimCreateRemote
	NewSimJoinRemote
)

type newSimType int32

func (n newSimType) String() string {
	return []string{
		"Create a local sim",
		"Create a sim on the public vice server",
		"Join a sim on the public vice server"}[n]
}

func (c *NewSimConfiguration) UIButtonText() string {
	return util.Select(c.newSimType == NewSimJoinRemote, "Join", "Next")
}

// ShowConfigurationWindow returns true if we should show the configuration screen
// (for create flows), false for join flow which goes directly to join.
func (c *NewSimConfiguration) ShowConfigurationWindow() bool {
	return c.newSimType != NewSimJoinRemote
}

// ScenarioSelectionDisabled returns true if the Next/Join button should be disabled
// on the scenario selection screen.
func (c *NewSimConfiguration) ScenarioSelectionDisabled(config *Config) bool {
	if c.newSimType == NewSimJoinRemote {
		// For join, need TCW selected and initials
		if c.selectedTCW == "" || len(config.ControllerInitials) != 2 {
			return true
		}
	}
	// For create flows, just need a valid scenario selected (no validation needed here)
	return false
}

// ConfigurationDisabled returns true if the Create button should be disabled
// on the configuration screen.
func (c *NewSimConfiguration) ConfigurationDisabled(config *Config) bool {
	if len(config.ControllerInitials) != 2 {
		return true
	}
	return c.newSimType == NewSimCreateRemote && (c.NewSimName == "" || (c.RequirePassword && c.Password == ""))
}

// getARTCCForFacility returns the ARTCC code for a given facility.
func getARTCCForFacility(facility string, catalog *server.ScenarioCatalog) string {
	if catalog != nil && catalog.ARTCC != "" {
		return catalog.ARTCC
	}
	if traconInfo, ok := av.DB.TRACONs[facility]; ok {
		return traconInfo.ARTCC
	}
	return facility
}

// trimFacilityName removes common suffixes from facility names for cleaner display.
func trimFacilityName(name, facilityType string) string {
	name = strings.TrimSpace(name)
	switch facilityType {
	case "TRACON":
		name = strings.TrimSuffix(name, " TRACON")
		name = strings.TrimSuffix(name, " ATCT/TRACON")
		name = strings.TrimSuffix(name, " Tower")
	case "ARTCC", "Area":
		name = strings.TrimSuffix(name, " ARTCC")
		name = strings.TrimSuffix(name, " Center")
	}
	return strings.TrimSpace(name)
}

// formatFacilityLabel returns a display label for a facility, including its full name if available.
func formatFacilityLabel(facility string) string {
	if traconInfo, ok := av.DB.TRACONs[facility]; ok {
		name := trimFacilityName(traconInfo.Name, "TRACON")
		if name == "" {
			return facility
		}
		return fmt.Sprintf("%s (%s)", facility, name)
	}
	if artccInfo, ok := av.DB.ARTCCs[facility]; ok {
		name := trimFacilityName(artccInfo.Name, "ARTCC")
		if name == "" {
			return facility
		}
		return fmt.Sprintf("%s (%s)", facility, name)
	}
	return facility
}

// getAreaKey returns the area identifier for grouping scenarios.
// For TRACONs, returns the groupName; for ARTCCs, returns the trimmed Area field.
func getAreaKey(facility, groupName string, catalog *server.ScenarioCatalog) string {
	if _, isTRACON := av.DB.TRACONs[facility]; isTRACON {
		return groupName
	}
	return trimFacilityName(catalog.Area, "Area")
}

// DrawScenarioSelectionUI draws Screen 1: scenario selection, sim type choice, and join flow UI
func (c *NewSimConfiguration) DrawScenarioSelectionUI(p platform.Platform, config *Config) bool {
	if err := c.mgr.UpdateRunningSims(); err != nil {
		c.lg.Warnf("UpdateRunningSims: %v", err)
	}

	if c.displayError != nil {
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .5, .5, 1})
		if errors.Is(c.displayError, server.ErrRPCTimeout) || util.IsRPCServerError(c.displayError) {
			imgui.Text("Unable to reach vice server")
		} else if errors.Is(c.displayError, server.ErrInvalidPassword) {
			imgui.Text("Invalid password entered")
		} else {
			imgui.Text(c.displayError.Error())
		}
		imgui.PopStyleColor()
		imgui.Separator()
	}

	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	var runningSims map[string]*server.RunningSim
	if c.mgr.RemoteServer != nil {
		runningSims = c.mgr.RemoteServer.GetRunningSims()

		if imgui.BeginTableV("server", 2, 0, imgui.Vec2{tableScale * 500, 0}, 0.) {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Sim options:")

			origType := c.newSimType

			doButton := func(ty newSimType, srv *client.Server) {
				if imgui.RadioButtonIntPtr(ty.String(), (*int32)(&c.newSimType), int32(ty)) && origType != ty {
					c.selectedServer = srv
					c.SetFacility(c.Facility)
					c.displayError = nil
				}
			}

			imgui.TableNextColumn()
			doButton(NewSimCreateLocal, c.mgr.LocalServer)

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TableNextColumn()
			doButton(NewSimCreateRemote, c.mgr.RemoteServer)

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TableNextColumn()

			if len(runningSims) == 0 {
				imgui.BeginDisabled()
				if c.newSimType == NewSimJoinRemote {
					c.newSimType = NewSimCreateRemote
				}
			}
			doButton(NewSimJoinRemote, c.mgr.RemoteServer)
			if len(runningSims) == 0 {
				imgui.EndDisabled()
			}

			imgui.EndTable()
		}
	} else {
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .5, .5, 1})
		imgui.Text("Unable to connect to the vice server; only local scenarios are available.")
		imgui.PopStyleColor()
		c.newSimType = NewSimCreateLocal
	}
	imgui.Separator()

	// Helper types and functions for facility data access and formatting
	const indentSpaces = "  "

	type areaInfo struct {
		area       string
		groupNames []string
	}

	type scenarioInfo struct {
		groupName    string
		scenarioName string
		spec         *server.ScenarioSpec
	}

	if c.newSimType == NewSimCreateLocal || c.newSimType == NewSimCreateRemote {
		tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))

		// Search/filter input
		filterW := tableScale*700 - 60

		imgui.SetNextItemWidth(filterW)
		imgui.InputTextWithHint("##filter", "Search scenarios, TRACONs, ARTCCs...", &c.filterText, 0, nil)
		imgui.SameLine()
		if imgui.Button("Clear") {
			c.filterText = ""
		}
		imgui.Spacing()

		// Precompute lowercased filter text once for all filter checks
		filterLower := strings.ToLower(c.filterText)

		// Helper to check if text matches filter
		matchesFilter := func(text string) bool {
			if filterLower == "" {
				return true
			}
			return strings.Contains(strings.ToLower(text), filterLower)
		}

		// Helper to check if a catalog has matching airports
		catalogHasMatchingAirport := func(catalog *server.ScenarioCatalog) bool {
			return filterLower == "" || util.SeqContainsFunc(slices.Values(catalog.Airports),
				func(ap string) bool { return strings.Contains(strings.ToLower(ap), filterLower) })
		}

		// Helper to check if a catalog has matching scenario names
		catalogHasMatchingScenario := func(catalog *server.ScenarioCatalog) bool {
			return filterLower == "" || util.SeqContainsFunc(maps.Keys(catalog.Scenarios),
				func(scenarioName string) bool { return strings.Contains(strings.ToLower(scenarioName), filterLower) })
		}

		// Helper to check if a catalog matches the filter (name, facility, airports, or scenarios)
		catalogMatchesFilter := func(catalog *server.ScenarioCatalog) bool {
			if filterLower == "" {
				return true
			}
			// Check airports in the catalog
			if catalogHasMatchingAirport(catalog) {
				return true
			}
			// Check facility name
			if matchesFilter(catalog.Facility) {
				return true
			}
			// Check scenario names
			if catalogHasMatchingScenario(catalog) {
				return true
			}
			return false
		}

		// Helper to check if any catalog in a facility matches
		facilityMatchesFilter := func(facility string, catalogs map[string]*server.ScenarioCatalog) bool {
			if filterLower == "" {
				return true
			}
			// Check facility name
			if matchesFilter(facility) {
				return true
			}
			// Check catalogs (airports and scenario names)
			for _, catalog := range catalogs {
				if catalogHasMatchingAirport(catalog) || catalogHasMatchingScenario(catalog) {
					return true
				}
			}
			return false
		}

		flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg |
			imgui.TableFlagsSizingStretchProp
		if imgui.BeginTableV("SelectScenario", 3, flags, imgui.Vec2{tableScale * 700, tableScale * 500}, 0.) {
			imgui.TableSetupColumn("ARTCC")
			imgui.TableSetupColumn("TRACON/AREA")
			imgui.TableSetupColumn("Scenario")
			imgui.TableHeadersRow()
			imgui.TableNextRow()

			// Build facility data structures
			catalogsByFacility := c.selectedServer.GetScenarioCatalogs()
			allFacilities := util.SortedMapKeys(catalogsByFacility)
			facilityCatalogs := make(map[string]*server.ScenarioCatalog, len(catalogsByFacility))
			for facility, catalogs := range catalogsByFacility {
				for _, cfg := range catalogs {
					facilityCatalogs[facility] = cfg
					break
				}
			}

			// Collect unique ARTCCs and track which ones match the filter
			artccs := make(map[string]struct{})
			matchingARTCCs := make(map[string]struct{})
			matchingFacilities := make(map[string]struct{})
			// Track groups that have matching scenarios specifically
			type facilityGroup struct {
				facility  string
				groupName string
			}
			var matchingGroups []facilityGroup
			// Helper to check if an ARTCC matches the filter
			artccMatchesFilter := func(artcc string) bool {
				if filterLower == "" {
					return true
				}
				if matchesFilter(artcc) {
					return true
				}
				// Also check the ARTCC's full name
				if artccInfo, ok := av.DB.ARTCCs[artcc]; ok {
					if matchesFilter(artccInfo.Name) {
						return true
					}
				}
				return false
			}

			for facility, catalogs := range catalogsByFacility {
				info := facilityCatalogs[facility]
				if info == nil {
					continue
				}
				artcc := getARTCCForFacility(facility, info)
				artccs[artcc] = struct{}{}

				// Check if this facility matches the filter (including ARTCC name)
				if facilityMatchesFilter(facility, catalogs) || artccMatchesFilter(artcc) {
					matchingARTCCs[artcc] = struct{}{}
					matchingFacilities[facility] = struct{}{}
				}

				// Track groups with matching scenarios
				if filterLower != "" {
					for groupName, catalog := range catalogs {
						if catalogHasMatchingScenario(catalog) {
							matchingGroups = append(matchingGroups, facilityGroup{facility, groupName})
						}
					}
				}
			}

			// Auto-select ARTCC if only one matches the filter
			selectedARTCC := ""
			if c.Facility != "" {
				selectedARTCC = getARTCCForFacility(c.Facility, facilityCatalogs[c.Facility])
			}
			if filterLower != "" && len(matchingARTCCs) == 1 {
				for artcc := range matchingARTCCs {
					if artcc != selectedARTCC {
						// Find first matching facility in this ARTCC and select it
						for facility := range matchingFacilities {
							if getARTCCForFacility(facility, facilityCatalogs[facility]) == artcc {
								c.SetFacility(facility)
								selectedARTCC = artcc
								break
							}
						}
					}
					break
				}
			}

			// Auto-select facility if only one matches within the selected ARTCC
			if filterLower != "" && selectedARTCC != "" {
				var matchingInARTCC []string
				for facility := range matchingFacilities {
					if getARTCCForFacility(facility, facilityCatalogs[facility]) == selectedARTCC {
						matchingInARTCC = append(matchingInARTCC, facility)
					}
				}
				if len(matchingInARTCC) == 1 && matchingInARTCC[0] != c.Facility {
					c.SetFacility(matchingInARTCC[0])
				}
			}

			// Ensure we have a group with matching scenarios selected, but only if the
			// current ARTCC doesn't have any matching facilities (respect user's ARTCC choice)
			_, currentARTCCHasMatches := matchingARTCCs[selectedARTCC]
			if filterLower != "" && len(matchingGroups) > 0 && !currentARTCCHasMatches {
				// Sort for deterministic selection
				sort.Slice(matchingGroups, func(i, j int) bool {
					if matchingGroups[i].facility != matchingGroups[j].facility {
						return matchingGroups[i].facility < matchingGroups[j].facility
					}
					return matchingGroups[i].groupName < matchingGroups[j].groupName
				})
				fg := matchingGroups[0]
				c.SetFacility(fg.facility)
				c.SetScenario(fg.groupName, c.selectedFacilityCatalogs[fg.groupName].DefaultScenario)
				selectedARTCC = getARTCCForFacility(fg.facility, facilityCatalogs[fg.facility])
			}

			// Calculate proportional column widths: 25%, 25%, 50%
			totalWidth := tableScale * 700
			artccWidth := max(totalWidth*0.25, tableScale*170)
			traconWidth := max(totalWidth*0.25, tableScale*160)
			scenarioWidth := max(totalWidth*0.50, tableScale*280)
			columnHeight := tableScale * 480

			// Column 1: ARTCC list
			imgui.TableNextColumn()
			if imgui.BeginChildStrV("artccs", imgui.Vec2{artccWidth, columnHeight}, 0, 0) {
				for artcc := range util.SortedMap(artccs) {
					name := trimFacilityName(av.DB.ARTCCs[artcc].Name, "ARTCC")
					if name == "" {
						name = artcc
					}
					label := fmt.Sprintf("%s (%s)", artcc, name)
					// Filter: show if name matches or if any facility in this ARTCC has matching airports
					_, artccMatches := matchingARTCCs[artcc]
					if filterLower != "" && !artccMatches && !matchesFilter(artcc) && !matchesFilter(name) {
						continue
					}
					if imgui.SelectableBoolV(label, artcc == selectedARTCC, 0, imgui.Vec2{}) && artcc != selectedARTCC {
						// Find first matching facility in this ARTCC
						var facilityToSelect string
						for facility := range matchingFacilities {
							if getARTCCForFacility(facility, facilityCatalogs[facility]) == artcc {
								facilityToSelect = facility
								break
							}
						}
						if facilityToSelect == "" {
							// No matching facility, just pick the first one
							for _, facility := range allFacilities {
								if getARTCCForFacility(facility, facilityCatalogs[facility]) == artcc {
									facilityToSelect = facility
									break
								}
							}
						}
						if facilityToSelect != "" {
							c.SetFacility(facilityToSelect)
							selectedARTCC = artcc // Update for this frame
						}
					}
				}
			}
			imgui.EndChild()

			// Column 2: TRACONs or ARTCC areas for selected ARTCC
			imgui.TableNextColumn()
			if imgui.BeginChildStrV("tracons/areas", imgui.Vec2{traconWidth, columnHeight}, 0, 0) {
				for _, facility := range allFacilities {
					info := facilityCatalogs[facility]
					if info == nil {
						continue
					}
					artcc := getARTCCForFacility(facility, info)
					if selectedARTCC != "" && artcc != selectedARTCC {
						continue
					}

					// Build area/group structure for this facility, only including matching catalogs
					catalogs := catalogsByFacility[facility]
					_, isTRACON := av.DB.TRACONs[facility]
					areaToGroups := make(map[string]*areaInfo)

					for groupName, gcfg := range catalogs {
						// Skip catalogs that don't match the filter (unless ARTCC matches)
						if filterLower != "" && !catalogMatchesFilter(gcfg) && !artccMatchesFilter(artcc) {
							continue
						}
						area := getAreaKey(facility, groupName, gcfg)
						if areaToGroups[area] == nil {
							areaToGroups[area] = &areaInfo{area: area}
						}
						areaToGroups[area].groupNames = append(areaToGroups[area].groupNames, groupName)
					}

					if len(areaToGroups) == 0 {
						continue
					}

					// Display facility label
					label := formatFacilityLabel(facility)
					if imgui.SelectableBoolV(label, facility == c.Facility, 0, imgui.Vec2{}) && facility != c.Facility {
						c.SetFacility(facility)
					}

					// Display sub-items (groups for TRACONs, areas for ARTCCs)
					if facility == c.Facility {
						sortedAreas := util.SortedMapKeys(areaToGroups)
						sort.Strings(sortedAreas)
						for _, areaKey := range sortedAreas {
							aInfo := areaToGroups[areaKey]
							// For TRACONs, just show group names; for ARTCCs, show area names
							itemLabel := indentSpaces + aInfo.area
							if !isTRACON && aInfo.area == "" {
								itemLabel = indentSpaces + aInfo.groupNames[0]
							}
							// Check if any group in this area/item is selected
							selected := slices.Contains(aInfo.groupNames, c.GroupName)
							if imgui.SelectableBoolV(itemLabel, selected, 0, imgui.Vec2{}) {
								firstGroup := aInfo.groupNames[0]
								if firstGroup != c.GroupName {
									c.SetScenario(firstGroup, catalogs[firstGroup].DefaultScenario)
								}
							}
						}
					}
				}
			}
			imgui.EndChild()

			// Column 3: Scenarios for the selected TRACON or area
			imgui.TableNextColumn()
			if imgui.BeginChildStrV("scenarios", imgui.Vec2{scenarioWidth, columnHeight}, 0, 0) {
				selectedCatalog := c.selectedFacilityCatalogs[c.GroupName]
				if selectedCatalog != nil {
					selectedArea := getAreaKey(c.Facility, c.GroupName, selectedCatalog)

					// Collect all scenarios from groups with the same area
					type scenarioWithCatalog struct {
						scenarioInfo
						catalog *server.ScenarioCatalog
					}
					var allScenarios []scenarioWithCatalog
					for groupName, group := range c.selectedFacilityCatalogs {
						if getAreaKey(c.Facility, groupName, group) == selectedArea {
							for name, spec := range group.Scenarios {
								allScenarios = append(allScenarios, scenarioWithCatalog{
									scenarioInfo: scenarioInfo{
										groupName:    groupName,
										scenarioName: name,
										spec:         spec,
									},
									catalog: group,
								})
							}
						}
					}

					// Sort and display scenarios
					sort.Slice(allScenarios, func(i, j int) bool {
						return allScenarios[i].scenarioName < allScenarios[j].scenarioName
					})
					for _, s := range allScenarios {
						// Filter scenarios: show if this specific scenario name matches, OR
						// if the catalog has a matching airport/facility name (but NOT because
						// another scenario in the catalog matches), OR if the ARTCC matches
						if filterLower != "" &&
							!matchesFilter(s.scenarioName) &&
							!catalogHasMatchingAirport(s.catalog) &&
							!matchesFilter(s.catalog.Facility) &&
							!artccMatchesFilter(selectedARTCC) {
							continue
						}
						selected := s.groupName == c.GroupName && s.scenarioName == c.ScenarioName
						if imgui.SelectableBoolV(s.scenarioName, selected, 0, imgui.Vec2{}) {
							c.SetScenario(s.groupName, s.scenarioName)
						}
					}
				}
			}
			imgui.EndChild()

			imgui.EndTable()
		}

		if len(c.ScenarioSpec.ArrivalRunways) > 0 {
			var a []string
			for _, rwy := range c.ScenarioSpec.ArrivalRunways {
				a = append(a, rwy.Airport+"/"+string(rwy.Runway))
			}
			sort.Strings(a)
			base := "Landing: "
			for len(a) > 0 {
				const max = 7 // per line
				if len(a) > max {
					imgui.Text(base + strings.Join(a[:max], ", "))
					base = "    "
					a = a[max:]
				} else {
					imgui.Text(base + strings.Join(a, ", "))
					break
				}
			}
		}
		// Configuration options (initials, checkboxes, METAR) are now on Screen 2
	} else {
		// Join remote
		rs, ok := runningSims[c.joinRequest.SimName]
		if !ok || c.joinRequest.SimName == "" {
			c.joinRequest.SimName, rs = util.FirstSortedMapEntry(runningSims)
		}
		controllersForGroup := controlPositionsForGroup(c.selectedServer, rs.GroupName)

		imgui.Text("Available simulations:")
		flags := imgui.TableFlagsBordersH | imgui.TableFlagsBordersOuterV | imgui.TableFlagsRowBg |
			imgui.TableFlagsSizingFixedFit
		if imgui.BeginTableV("simulation", 4, flags, imgui.Vec2{tableScale * 700, 0}, 0.) {
			imgui.TableSetupColumn("") // lock
			imgui.TableSetupColumn("Name")
			imgui.TableSetupColumn("Configuration")
			imgui.TableSetupColumn("Controllers")
			imgui.TableHeadersRow()

			for simName, rs := range util.SortedMap(runningSims) {
				imgui.PushIDStr(simName)
				imgui.TableNextRow()
				imgui.TableNextColumn()

				// Indicate if a password is required
				if rs.RequirePassword {
					imgui.Text(renderer.FontAwesomeIconLock)
				}
				imgui.TableNextColumn()

				selected := simName == c.joinRequest.SimName
				selFlags := imgui.SelectableFlagsSpanAllColumns | imgui.SelectableFlagsNoAutoClosePopups
				if imgui.SelectableBoolV(simName, selected, selFlags, imgui.Vec2{}) {
					c.joinRequest.SimName = simName
					// Reset TCW selection when switching sims
					c.selectedTCW = ""
					c.selectedTCPs = nil
				}

				imgui.TableNextColumn()
				imgui.Text(runningSims[simName].ScenarioName)

				imgui.TableNextColumn()
				// Count occupied vs total TCWs
				var occupied, total int
				var occupiedTCWs []string
				for tcw, state := range rs.CurrentConsolidation {
					total++
					if state.IsOccupied() {
						occupied++
						occupiedTCWs = append(occupiedTCWs,
							controllerDisplayLabel(controllersForGroup, av.ControlPosition(tcw)),
						)
					}
				}
				controllers := fmt.Sprintf("%d / %d", occupied, total)
				imgui.Text(controllers)
				if imgui.IsItemHovered() && occupied > 0 {
					slices.Sort(occupiedTCWs)
					imgui.SetTooltip(strings.Join(occupiedTCWs, ", "))
				}

				imgui.PopID()
			}
			imgui.EndTable()
		}

		// Handle the case where selected TCW is no longer valid
		if c.selectedTCW != "" {
			if state, ok := rs.CurrentConsolidation[c.selectedTCW]; ok {
				// Check if TCW is still valid for current mode
				if c.showReliefPositions && !state.IsOccupied() {
					c.selectedTCW = ""
				} else if !c.showReliefPositions && state.IsOccupied() {
					c.selectedTCW = ""
				}
			} else {
				c.selectedTCW = ""
			}
		}

		// Format TCPs for display (SSA style: "primary *sec1 sec2")
		fmtTCPs := func(cons server.TCPConsolidation) string {
			result := controllerDisplayLabel(controllersForGroup, av.ControlPosition(cons.PrimaryTCP))
			for _, sec := range cons.SecondaryTCPs {
				prefix := ""
				if sec.Type == sim.ConsolidationBasic {
					prefix = "*"
				}
				result += " " + prefix +
					controllerDisplayLabel(controllersForGroup, av.ControlPosition(sec.TCP))
			}
			return result
		}

		// Compute covered TCPs (primary at an occupied TCW)
		coveredPrimaryTCPs := make(map[sim.TCP]bool)
		for _, cons := range rs.CurrentConsolidation {
			if cons.PrimaryTCP != "" && cons.IsOccupied() {
				coveredPrimaryTCPs[cons.PrimaryTCP] = true
			}
		}

		// getAvailableTCPs returns all TCPs that can be selected:
		// - All positions (primary + secondary) from unoccupied TCWs
		// - Only secondary positions from occupied TCWs
		getAvailableTCPs := func() map[sim.TCP]bool {
			result := make(map[sim.TCP]bool)
			for _, cons := range rs.CurrentConsolidation {
				if cons.PrimaryTCP != "" && !cons.IsOccupied() {
					result[cons.PrimaryTCP] = true
				}
				for _, sec := range cons.SecondaryTCPs {
					result[sec.TCP] = true
				}
			}
			return result
		}

		// getDefaultSelectedTCPs returns the TCPs that should be selected by default for a TCW:
		// - Currently owned positions by the TCW (if any)
		// - Otherwise, just the position with the same name as the TCW
		getDefaultSelectedTCPs := func(tcw sim.TCW) map[sim.TCP]bool {
			result := make(map[sim.TCP]bool)
			cons := rs.CurrentConsolidation[tcw]
			if cons.PrimaryTCP != "" {
				result[cons.PrimaryTCP] = true
			}
			for _, sec := range cons.SecondaryTCPs {
				result[sec.TCP] = true
			}

			// If no positions found, default to just the TCW name
			if len(result) == 0 {
				result[sim.TCP(tcw)] = true
			}
			return result
		}

		// Checkbox for showing relief positions (only if some TCWs are occupied)
		if len(coveredPrimaryTCPs) > 0 {
			if imgui.Checkbox("Join as relief (show occupied positions)", &c.showReliefPositions) {
				// Clear selection when mode changes
				c.selectedTCW = ""
				c.selectedTCPs = nil
			}
			if imgui.IsItemHovered() {
				imgui.SetTooltip("Relief sign-in shares control with existing controller")
			}
		}

		// Sign-on options table
		imgui.Spacing()
		tableFlags := imgui.TableFlagsSizingFixedFit
		if imgui.BeginTableV("signon_options", 2, tableFlags, imgui.Vec2{}, 0) {
			imgui.TableSetupColumn("Label")
			imgui.TableSetupColumn("Value")

			// Row 1: Select TCW
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Select TCW:")
			imgui.TableNextColumn()
			first := true
			for tcw, cons := range util.SortedMap(rs.CurrentConsolidation) {
				// Filter: relief shows only occupied, normal shows only unoccupied
				if c.showReliefPositions != cons.IsOccupied() {
					continue
				}
				// Skip internal positions
				if len(tcw) > 0 && tcw[0] == '_' {
					continue
				}

				if !first {
					imgui.SameLine()
				}
				first = false

				label := controllerDisplayLabel(controllersForGroup, av.ControlPosition(tcw))
				selected := tcw == c.selectedTCW
				if imgui.RadioButtonBool(fmt.Sprintf("%s##tcw-%s", label, tcw), selected) {
					c.selectedTCW = tcw
					c.joinRequest.JoiningAsRelief = c.showReliefPositions
					// Initialize selected TCPs from TCW's current positions
					if !c.showReliefPositions {
						c.selectedTCPs = getDefaultSelectedTCPs(tcw)
					} else {
						c.selectedTCPs = nil
					}
				}
				// Tooltip shows positions (and controller for relief mode)
				if c.showReliefPositions && imgui.IsItemHovered() {
					tooltip := fmtTCPs(cons)
					if len(cons.Initials) > 0 {
						tooltip += " (" + strings.Join(cons.Initials, ", ") + ")"
					}
					imgui.SetTooltip(tooltip)
				}
			}

			// Row 2: Select positions (only for unoccupied TCW selection, not relief)
			if c.selectedTCW != "" && !c.showReliefPositions {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text("Select positions:")
				imgui.TableNextColumn()

				// Show all available TCPs (excludes primaries at occupied TCWs)
				availableTCPs := getAvailableTCPs()
				for tcp := range util.SortedMap(availableTCPs) {
					if len(tcp) > 0 && tcp[0] == '_' {
						continue
					}

					isSelected := c.selectedTCPs[tcp]
					label := controllerDisplayLabel(controllersForGroup, av.ControlPosition(tcp))
					if imgui.Checkbox(fmt.Sprintf("%s##tcp-%s", label, tcp), &isSelected) {
						if c.selectedTCPs == nil {
							c.selectedTCPs = make(map[sim.TCP]bool)
						}
						c.selectedTCPs[tcp] = isSelected
					}
					imgui.SameLine()
				}

				imgui.Checkbox("Instructor", &c.Privileged)
				if imgui.IsItemHovered() {
					imgui.SetTooltip("Allows control of any aircraft regardless of position ownership")
				}
			}

			// Row 3: Controller initials
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Controller initials:")
			imgui.TableNextColumn()
			imgui.SetNextItemWidth(50)
			initialsFlags := imgui.InputTextFlagsCharsUppercase | imgui.InputTextFlagsCallbackCharFilter | imgui.InputTextFlagsCallbackEdit
			imgui.InputTextWithHint("##initials", "XX", &config.ControllerInitials, initialsFlags,
				func(input imgui.InputTextCallbackData) int {
					if input.EventFlag()&imgui.InputTextFlagsCallbackCharFilter != 0 {
						if ch := input.EventChar(); ch < 'A' || ch > 'Z' {
							return 1
						}
					}
					if input.EventFlag()&imgui.InputTextFlagsCallbackEdit != 0 {
						if input.BufTextLen() > 2 {
							input.DeleteChars(2, input.BufTextLen()-2)
						}
					}
					return 0
				})
			if len(config.ControllerInitials) < 2 {
				imgui.SameLine()
				imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{.7, .1, .1, 1})
				imgui.Text(renderer.FontAwesomeIconExclamationTriangle + " Must enter initials")
				imgui.PopStyleColor()
			}

			// Row 4: Password (if required)
			if rs.RequirePassword {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text("Password:")
				imgui.TableNextColumn()
				imgui.InputTextWithHint("##pw", "", &c.joinRequest.Password, 0, nil)
			}

			imgui.EndTable()
		}

	}

	return false
}

// drawSectionHeader draws a styled section header
func drawSectionHeader(title string) {
	imgui.Spacing()
	imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{0.6, 0.8, 1.0, 1.0})
	imgui.Text(strings.ToUpper(title))
	imgui.PopStyleColor()
	imgui.Separator()
}

// DrawConfigurationUI draws Screen 2: configuration options and traffic rates (combined)
func (c *NewSimConfiguration) DrawConfigurationUI(p platform.Platform, config *Config) bool {
	if c.displayError != nil {
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .5, .5, 1})
		imgui.Text(c.displayError.Error())
		imgui.PopStyleColor()
		imgui.Separator()
	}

	// CONTROLLER SETTINGS section
	drawSectionHeader("Controller Settings")

	// Controller initials
	imgui.Text("Initials:")
	imgui.SameLine()
	imgui.SetNextItemWidth(50)
	initialsFlags := imgui.InputTextFlagsCharsUppercase | imgui.InputTextFlagsCallbackCharFilter | imgui.InputTextFlagsCallbackEdit
	imgui.InputTextWithHint("##initials", "XX", &config.ControllerInitials, initialsFlags,
		func(input imgui.InputTextCallbackData) int {
			if input.EventFlag()&imgui.InputTextFlagsCallbackCharFilter != 0 {
				if ch := input.EventChar(); ch < 'A' || ch > 'Z' {
					return 1
				}
			}
			if input.EventFlag()&imgui.InputTextFlagsCallbackEdit != 0 {
				if input.BufTextLen() > 2 {
					input.DeleteChars(2, input.BufTextLen()-2)
				}
			}
			return 0
		})
	if imgui.IsItemHovered() {
		imgui.SetTooltip("Enter two letters for controller initials")
	}
	if len(config.ControllerInitials) < 2 {
		imgui.SameLine()
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{.7, .1, .1, 1})
		imgui.Text(renderer.FontAwesomeIconExclamationTriangle + " Must enter initials")
		imgui.PopStyleColor()
	}

	if c.newSimType == NewSimCreateRemote {
		imgui.Checkbox("Sign in with instructor/RPO privileges", &c.Privileged)
	}
	imgui.Spacing()

	// SESSION OPTIONS section (remote only)
	if c.newSimType == NewSimCreateRemote {
		drawSectionHeader("Session Options")

		imgui.Text("Name: " + c.NewSimName)

		imgui.Checkbox("Require Password", &c.RequirePassword)
		if c.RequirePassword {
			imgui.SameLine()
			imgui.SetNextItemWidth(150)
			imgui.InputTextWithHint("##password", "Enter password", &c.Password, 0, nil)
			if c.Password == "" {
				imgui.SameLine()
				imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{.7, .1, .1, 1})
				imgui.Text(renderer.FontAwesomeIconExclamationTriangle)
				imgui.PopStyleColor()
			}
		}
		imgui.Spacing()
	}

	// SIMULATION SETTINGS section
	drawSectionHeader("Simulation Settings")

	imgui.Checkbox("Ensure unique callsign suffixes", &c.NewSimRequest.EnforceUniqueCallsignSuffix)

	imgui.Text("Readback error interval:")
	imgui.SameLine()
	imgui.SetNextItemWidth(200)
	imgui.SliderFloatV("##errorInterval", &c.PilotErrorInterval, 0, 30,
		util.Select(c.PilotErrorInterval == 0, "never", "%.1f min"), imgui.SliderFlagsNone)
	imgui.Spacing()

	// WEATHER & TIME section
	drawSectionHeader("Weather & Time")

	c.mu.Lock(c.lg)
	defer c.mu.Unlock(c.lg)

	if c.fetchMETARError != nil {
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .5, .5, 1})
		imgui.Text("Error: " + c.fetchMETARError.Error())
		imgui.PopStyleColor()
	} else if len(c.airportMETAR) > 0 {
		c.drawWeatherFilterUI()
	}
	imgui.Spacing()

	// TRAFFIC RATES section
	drawSectionHeader("Traffic Rates")

	// Rate limit warning
	const rateLimit = 100.0
	if !c.ScenarioSpec.LaunchConfig.CheckRateLimits(rateLimit) {
		c.ScenarioSpec.LaunchConfig.ClampRates(rateLimit)
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .5, .5, 1})
		imgui.Text(renderer.FontAwesomeIconExclamationTriangle + " Rates reduced to stay within limits")
		imgui.PopStyleColor()
	}

	// Departures (collapsible)
	lc := &c.ScenarioSpec.LaunchConfig
	if lc.HaveDepartures() {
		depRate := lc.TotalDepartureRate()
		headerText := fmt.Sprintf("Departures (Total: %d/hr)###departures", int(depRate+0.5))
		if imgui.CollapsingHeaderBoolPtr(headerText, nil) {
			drawDepartureUI(lc, p)
			imgui.Spacing()
		}
	}

	// VFR Departures (collapsible)
	if len(lc.VFRAirportRates) > 0 {
		vfrRate := 0
		for _, rate := range lc.VFRAirportRates {
			r := float32(rate) * lc.VFRDepartureRateScale
			if r > 0 {
				vfrRate += int(r)
			}
		}
		headerText := fmt.Sprintf("VFR Departures (%d/hr)###vfrdepartures", vfrRate)
		if imgui.CollapsingHeaderBoolPtr(headerText, nil) {
			drawVFRDepartureUI(lc, p)
			imgui.Spacing()
		}
	}

	// Arrivals (collapsible)
	if lc.HaveArrivals() {
		arrRate := lc.TotalArrivalRate()
		headerText := fmt.Sprintf("Arrivals (Total: %d/hr)###arrivals", int(arrRate+0.5))
		if imgui.CollapsingHeaderBoolPtr(headerText, nil) {
			drawArrivalUI(lc, p)
			imgui.Spacing()
		}
	}

	// Overflights (collapsible)
	if lc.HaveOverflights() {
		ofRate := lc.TotalOverflightRate()
		headerText := fmt.Sprintf("Overflights (%d/hr)###overflights", int(ofRate+0.5))
		if imgui.CollapsingHeaderBoolPtr(headerText, nil) {
			drawOverflightUI(lc, p)
			imgui.Spacing()
		}
	}

	// Emergency rate (always visible)
	imgui.Spacing()
	imgui.Text("Emergency aircraft rate:")
	imgui.SameLine()
	imgui.SetNextItemWidth(150)
	imgui.SliderFloatV("##emergencyRate", &lc.EmergencyAircraftRate, 0, 20,
		util.Select(lc.EmergencyAircraftRate == 0, "never", "%.1f /hr"), imgui.SliderFlagsNone)

	return false
}

func (c *NewSimConfiguration) DrawRatesUI(p platform.Platform) bool {
	// Check rate limits and clamp if necessary
	const rateLimit = 100.0
	rateClamped := false
	if !c.ScenarioSpec.LaunchConfig.CheckRateLimits(rateLimit) {
		c.ScenarioSpec.LaunchConfig.ClampRates(rateLimit)
		rateClamped = true
	}

	// Display error message if rates were clamped
	if rateClamped {
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .5, .5, 1})
		imgui.Text(fmt.Sprintf("Launch rates will be reduced to stay within the %d aircraft/hour limit", int(rateLimit)))
		imgui.PopStyleColor()
	}

	drawDepartureUI(&c.ScenarioSpec.LaunchConfig, p)
	drawVFRDepartureUI(&c.ScenarioSpec.LaunchConfig, p)
	drawArrivalUI(&c.ScenarioSpec.LaunchConfig, p)
	drawOverflightUI(&c.ScenarioSpec.LaunchConfig, p)
	drawEmergencyAircraftUI(&c.ScenarioSpec.LaunchConfig, p)
	return false
}

func (c *NewSimConfiguration) Start(config *Config) error {
	c.TFRs = c.tfrCache.TFRsForTRACON(c.Facility, c.lg)
	c.NewSimRequest.Emergencies = c.emergencies

	if c.newSimType == NewSimJoinRemote {
		// Set the privileged flag from the main config
		c.joinRequest.Privileged = c.Privileged
		// Set TCW from selection
		c.joinRequest.TCW = c.selectedTCW
		// Convert selected TCPs map to slice (only for non-relief)
		if !c.joinRequest.JoiningAsRelief {
			var tcps []sim.TCP
			for tcp, selected := range c.selectedTCPs {
				if selected {
					tcps = append(tcps, tcp)
				}
			}
			c.joinRequest.SelectedTCPs = tcps
		}
		c.joinRequest.Initials = config.ControllerInitials
		if err := c.mgr.ConnectToSim(c.joinRequest, config.ControllerInitials, c.selectedServer, c.lg); err != nil {
			c.lg.Errorf("ConnectToSim failed: %v", err)
			return err
		}
	} else {
		// Create sim configuration for new sim
		c.NewSimRequest.Initials = config.ControllerInitials
		if err := c.mgr.CreateNewSim(c.NewSimRequest, config.ControllerInitials, c.selectedServer, c.lg); err != nil {
			c.lg.Errorf("CreateNewSim failed: %v", err)
			return err
		}
	}

	*c.defaultFacility = c.Facility
	return nil
}

func drawDepartureUI(lc *sim.LaunchConfig, p platform.Platform) (changed bool) {
	if len(lc.DepartureRates) == 0 {
		return
	}

	airportDepartures := make(map[string]int) // key is e.g. KJFK, then count of active runways cross categories.
	for ap, runwayRates := range lc.DepartureRates {
		for _, categories := range runwayRates {
			airportDepartures[ap] = airportDepartures[ap] + len(categories)
		}
	}
	maxDepartureCategories := 0
	for _, n := range airportDepartures {
		maxDepartureCategories = max(n, maxDepartureCategories)
	}

	// SliderFlagsNoInput is more or less a hack to prevent keyboard focus
	// from being here initially.
	changed = imgui.SliderFloatV("Departure rate scale", &lc.DepartureRateScale, 0, 5, "%.1f", imgui.SliderFlagsNoInput) || changed

	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp

	if lc.DepartureRateScale == 0 {
		imgui.BeginDisabled()
	}
	adrColumns := min(3, maxDepartureCategories)
	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	if imgui.BeginTableV("departureRunways", int32(1+3*adrColumns), flags, imgui.Vec2{tableScale * float32(200+250*adrColumns), 0}, 0.) {
		imgui.TableSetupColumn("Airport")
		for range adrColumns {
			imgui.TableSetupColumn("Runway")
			imgui.TableSetupColumn("Category")
			imgui.TableSetupColumn("ADR")
		}
		imgui.TableHeadersRow()

		for airport := range util.SortedMap(lc.DepartureRates) {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text(airport)

			imgui.PushIDStr(airport)
			adrColumn := 0
			for runway := range util.SortedMap(lc.DepartureRates[airport]) {
				imgui.PushIDStr(string(runway))

				for category := range util.SortedMap(lc.DepartureRates[airport][runway]) {
					imgui.TableNextColumn()
					rshort := runway.Base() // don't include extras in the UI
					imgui.Text(rshort)
					imgui.TableNextColumn()

					imgui.PushIDStr(category)

					if category == "" {
						imgui.Text("(All)")
					} else {
						imgui.Text(category)
					}
					imgui.TableNextColumn()

					r := int32(lc.DepartureRateScale*lc.DepartureRates[airport][runway][category] + 0.5)
					if imgui.InputIntV("##adr", &r, 0, 120, 0) {
						lc.DepartureRates[airport][runway][category] = float32(r) / max(.01, lc.DepartureRateScale)
						changed = true
					}

					adrColumn++

					if adrColumn < airportDepartures[airport] && adrColumn%adrColumns == 0 {
						// Overflow
						imgui.TableNextRow()
						imgui.TableNextColumn()
					}

					imgui.PopID()
				}
				imgui.PopID()
			}
			imgui.PopID()
		}
		imgui.EndTable()
	}
	if lc.DepartureRateScale == 0 {
		imgui.EndDisabled()
	}

	imgui.Separator()

	return
}

func drawVFRDepartureUI(lc *sim.LaunchConfig, p platform.Platform) (changed bool) {
	if len(lc.VFRAirportRates) == 0 {
		return
	}

	// SliderFlagsNoInput is more or less a hack to prevent keyboard focus
	// from being here initially.
	changed = imgui.SliderFloatV("VFR departure rate scale", &lc.VFRDepartureRateScale, 0, 2, "%.1f", imgui.SliderFlagsNoInput) || changed

	if !lc.HaveVFRReportingRegions {
		imgui.BeginDisabled()
	}
	changed = imgui.InputIntV("Flight following request rate", &lc.VFFRequestRate, 0, 60, 0) || changed
	if !lc.HaveVFRReportingRegions {
		imgui.EndDisabled()
	}

	imgui.Separator()

	return
}

func drawArrivalUI(lc *sim.LaunchConfig, p platform.Platform) (changed bool) {
	// Figure out the maximum number of inbound flows per airport to figure
	// out the number of table columns and also sum up the overall arrival
	// rate.
	numAirportFlows := make(map[string]int)
	for _, agr := range lc.InboundFlowRates {
		for ap := range agr {
			if ap != "overflights" {
				numAirportFlows[ap] = numAirportFlows[ap] + 1
			}
		}
	}
	if len(numAirportFlows) == 0 { // no arrivals
		return
	}
	maxAirportFlows := 0
	for _, n := range numAirportFlows {
		maxAirportFlows = max(n, maxAirportFlows)
	}

	changed = imgui.SliderFloatV("Arrival/overflight rate scale", &lc.InboundFlowRateScale, 0, 5, "%.1f", imgui.SliderFlagsNoInput) || changed

	changed = imgui.SliderFloatV("Go around probability", &lc.GoAroundRate, 0, 1, "%.02f", 0) || changed

	changed = imgui.Checkbox("Include random arrival pushes", &lc.ArrivalPushes) || changed
	if !lc.ArrivalPushes {
		imgui.BeginDisabled()
	}
	freq := int32(lc.ArrivalPushFrequencyMinutes)
	changed = imgui.SliderInt("Push frequency (minutes)", &freq, 3, 60) || changed
	lc.ArrivalPushFrequencyMinutes = int(freq)
	mins := int32(lc.ArrivalPushLengthMinutes)
	changed = imgui.SliderInt("Length of push (minutes)", &mins, 5, 30) || changed
	lc.ArrivalPushLengthMinutes = int(mins)
	if !lc.ArrivalPushes {
		imgui.EndDisabled()
	}

	aarColumns := min(3, maxAirportFlows)
	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	if lc.InboundFlowRateScale == 0 {
		imgui.BeginDisabled()
	}
	if imgui.BeginTableV("arrivalgroups", int32(1+2*aarColumns), flags, imgui.Vec2{tableScale * float32(150+250*aarColumns), 0}, 0.) {
		imgui.TableSetupColumn("Airport")
		for range aarColumns {
			imgui.TableSetupColumn("Arrival")
			imgui.TableSetupColumn("AAR")
		}
		imgui.TableHeadersRow()

		for ap := range util.SortedMap(numAirportFlows) {
			imgui.PushIDStr(ap)
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text(ap)

			aarCol := 0
			for group, aprates := range util.SortedMap(lc.InboundFlowRates) {
				imgui.PushIDStr(group)
				if rate, ok := aprates[ap]; ok {
					if aarCol > 0 && aarCol%aarColumns == 0 {
						// Overflow
						imgui.TableNextRow()
						imgui.TableNextColumn()
					}

					imgui.TableNextColumn()
					imgui.Text(group)
					imgui.TableNextColumn()
					r := int32(rate*lc.InboundFlowRateScale + 0.5)
					if imgui.InputIntV("##aar-"+ap, &r, 0, 120, 0) {
						changed = true
						lc.InboundFlowRates[group][ap] = float32(r) / max(.01, lc.InboundFlowRateScale)
					}
					aarCol++

				}
				imgui.PopID()
			}
			imgui.PopID()
		}
		imgui.EndTable()
	}
	if lc.InboundFlowRateScale == 0 {
		imgui.EndDisabled()
	}

	imgui.Separator()

	return
}

func drawOverflightUI(lc *sim.LaunchConfig, p platform.Platform) (changed bool) {
	// Sum up the overall overflight rate
	overflightGroups := make(map[string]any)
	for group, rates := range lc.InboundFlowRates {
		if _, ok := rates["overflights"]; ok {
			overflightGroups[group] = nil
		}
	}
	if len(overflightGroups) == 0 {
		return
	}

	ofColumns := min(3, len(overflightGroups))
	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	if lc.InboundFlowRateScale == 0 {
		imgui.BeginDisabled()
	}
	if imgui.BeginTableV("overflights", int32(2*ofColumns), flags, imgui.Vec2{tableScale * float32(250*ofColumns), 0}, 0.) {
		for range ofColumns {
			imgui.TableSetupColumn("Group")
			imgui.TableSetupColumn("Rate")
		}
		imgui.TableHeadersRow()

		ofCol := 0
		for group := range util.SortedMap(overflightGroups) {
			imgui.PushIDStr(group)
			if ofCol%ofColumns == 0 {
				imgui.TableNextRow()
			}

			imgui.TableNextColumn()
			imgui.Text(group)
			imgui.TableNextColumn()
			r := int32(lc.InboundFlowRates[group]["overflights"]*lc.InboundFlowRateScale + 0.5)
			if imgui.InputIntV("##of-"+group, &r, 0, 120, 0) {
				changed = true
				lc.InboundFlowRates[group]["overflights"] = float32(r) / max(.01, lc.InboundFlowRateScale)
			}
			ofCol++

			imgui.PopID()
		}
		imgui.EndTable()
	}
	if lc.InboundFlowRateScale == 0 {
		imgui.EndDisabled()
	}

	return
}

func drawEmergencyAircraftUI(lc *sim.LaunchConfig, p platform.Platform) {
	imgui.SliderFloatV("Emergency Aircraft Rate (per hour)", &lc.EmergencyAircraftRate,
		0, 20, util.Select(lc.EmergencyAircraftRate == 0, "never", "%.1f"), imgui.SliderFlagsNone)
}

func controllerDisplayLabel(controllers map[av.ControlPosition]*av.Controller, pos av.ControlPosition) string {
	if ctrl, ok := controllers[pos]; ok && ctrl != nil {
		if label := ctrl.ERAMID(); label != "" {
			return label
		}
	}
	return string(pos)
}

func controlPositionsForGroup(server *client.Server, groupName string) map[sim.TCP]*av.Controller {
	if server == nil || groupName == "" {
		return nil
	}
	for _, groups := range server.GetScenarioCatalogs() {
		if catalog, ok := groups[groupName]; ok {
			return catalog.ControlPositions
		}
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////

var acknowledgedATIS = make(map[string]string)

func drawScenarioInfoWindow(config *Config, c *client.ControlClient, activeRadarPane panes.Pane, p platform.Platform, lg *log.Logger) bool {
	// Ensure that the window is wide enough to show the description
	sz := imgui.CalcTextSize(c.State.SimDescription)
	imgui.SetNextWindowSizeConstraints(imgui.Vec2{sz.X + 50, 0}, imgui.Vec2{100000, 100000})

	show := true
	imgui.BeginV(c.State.SimDescription+"###ScenarioInfo", &show, imgui.WindowFlagsAlwaysAutoResize)

	if imgui.CollapsingHeaderBoolPtr("Controllers", nil) {
		// Make big(ish) tables somewhat more legible
		tableFlags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH |
			imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
		if imgui.BeginTableV("controllers", 4, tableFlags, imgui.Vec2{}, 0) {
			imgui.TableSetupColumn("Workstation")
			imgui.TableSetupColumn("Name")
			imgui.TableSetupColumn("Human")
			imgui.TableSetupColumn("Positions")
			imgui.TableHeadersRow()

			// First the potentially-human-controlled ones
			tcws := slices.Collect(maps.Keys(c.State.CurrentConsolidation))
			slices.Sort(tcws)
			coveredPositions := make(map[av.ControlPosition]struct{})
			for _, tcw := range tcws {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(controllerDisplayLabel(c.State.Controllers, av.ControlPosition(tcw)))

				imgui.TableNextColumn()
				imgui.Text(c.State.Controllers[av.ControlPosition(tcw)].Callsign)

				imgui.TableNextColumn()
				sq := renderer.FontAwesomeIconCheckSquare
				// Center the square in the column: https://stackoverflow.com/a/66109051
				pos := imgui.CursorPosX() + float32(imgui.ColumnWidth()) - imgui.CalcTextSize(sq).X - imgui.ScrollX() -
					2*imgui.CurrentStyle().ItemSpacing().X
				if pos > imgui.CursorPosX() {
					imgui.SetCursorPos(imgui.Vec2{X: pos, Y: imgui.CursorPos().Y})
				}
				imgui.Text(sq)

				imgui.TableNextColumn()
				if cons, ok := c.State.CurrentConsolidation[tcw]; ok {
					var p []string
					for _, pos := range cons.OwnedPositions() {
						coveredPositions[pos] = struct{}{}
						ctrl := c.State.Controllers[pos]
						p = append(p, fmt.Sprintf("%s (%s, %s)",
							controllerDisplayLabel(c.State.Controllers, ctrl.PositionId()),
							ctrl.Position,
							ctrl.Frequency.String(),
						))
					}

					s := ""
					for len(p) > 3 {
						s += strings.Join(p[:3], ", ") + "\n"
						p = p[3:]
					}
					s += strings.Join(p, ", ")
					imgui.Text(s)
				}
			}

			// Sort 2-char before 3-char and then alphabetically
			sorted := slices.Collect(maps.Keys(c.State.Controllers))
			slices.SortFunc(sorted, func(a, b sim.TCP) int {
				if len(a) < len(b) {
					return -1
				} else if len(a) > len(b) {
					return 1
				} else {
					return strings.Compare(string(a), string(b))
				}
			})

			for _, pos := range sorted {
				if _, ok := coveredPositions[pos]; ok {
					continue
				}

				ctrl := c.State.Controllers[pos]
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(controllerDisplayLabel(c.State.Controllers, ctrl.PositionId()))
				imgui.TableNextColumn()
				imgui.Text(ctrl.Callsign)
				imgui.TableNextColumn()
				imgui.TableNextColumn()
				imgui.Text(fmt.Sprintf("%s (%s, %s)",
					controllerDisplayLabel(c.State.Controllers, ctrl.PositionId()),
					ctrl.Position,
					ctrl.Frequency.String(),
				))
			}

			imgui.EndTable()
		}
	}

	if len(c.State.METAR) > 0 {
		// Collect IFR airports: those with IFR departures or arrivals
		ifrAirports := make(map[string]bool)
		for ap := range c.State.LaunchConfig.DepartureRates {
			ifrAirports[ap] = true
		}
		for ap := range c.State.ArrivalAirports {
			ifrAirports[ap] = true
		}

		atisExpanded := imgui.CollapsingHeaderBoolPtr("ATIS / METAR", nil)
		if atisExpanded {
			tableFlags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH |
				imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
			if imgui.BeginTableV("atis_metar", 2, tableFlags, imgui.Vec2{}, 0) {
				imgui.TableSetupColumnV("ATIS", imgui.TableColumnFlagsWidthFixed, 0, 0)
				imgui.TableSetupColumn("METAR")
				imgui.TableHeadersRow()

				airports := slices.Sorted(maps.Keys(c.State.METAR))
				for _, ap := range airports {
					if !ifrAirports[ap] {
						continue
					}
					letter := c.State.ATISLetter[ap]
					metar := c.State.METAR[ap]

					imgui.TableNextRow()
					imgui.TableNextColumn()

					// Flash if ATIS letter changed since last acknowledgement
					if _, ok := acknowledgedATIS[ap]; !ok {
						acknowledgedATIS[ap] = letter
					}
					ui.fixedFont.ImguiPush()
					flashing := acknowledgedATIS[ap] != letter
					if flashing && int64(imgui.Time()*2)%2 == 0 {
						imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .2, .2, 1})
					}
					// Center the letter in the column
					colW := imgui.ColumnWidth()
					textW := imgui.CalcTextSize(letter).X
					pad := (colW - textW) / 2
					if pad > 0 {
						imgui.SetCursorPosX(imgui.CursorPosX() + pad)
					}
					if imgui.SelectableBoolV(letter+"##atis_"+ap, false, 0, imgui.Vec2{}) {
						acknowledgedATIS[ap] = letter
					}
					if flashing && int64(imgui.Time()*2)%2 == 0 {
						imgui.PopStyleColor()
					}

					imgui.TableNextColumn()
					raw := strings.TrimPrefix(metar.Raw, "METAR ")
					raw = strings.TrimPrefix(raw, "SPECI ")
					imgui.Text(raw)
					imgui.PopFont()
				}

				imgui.EndTable()
			}

		}
	}

	if draw, ok := activeRadarPane.(panes.InfoWindowDrawer); ok {
		draw.DrawInfo(c, p, lg)
	}
	imgui.End()

	return show
}

// drawWeatherFilterUI draws the weather filter controls organized into logical groups
func (c *NewSimConfiguration) drawWeatherFilterUI() {
	const inputWidth float32 = 50
	changed := false

	// Helper to convert *int to string for display
	intPtrToStr := func(v *int) string {
		if v == nil {
			return ""
		}
		return strconv.Itoa(*v)
	}

	// Helper to parse string to *int
	parseOptionalInt := func(s string) *int {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		if v, err := strconv.Atoi(s); err == nil {
			return &v
		}
		return nil
	}

	// Helper for optional int input fields, returns true if changed
	optionalIntInput := func(label, hint string, value **int) bool {
		s := intPtrToStr(*value)
		imgui.SetNextItemWidth(inputWidth)
		if imgui.InputTextWithHint(label, hint, &s, 0, nil) {
			*value = parseOptionalInt(s)
			return true
		}
		return false
	}

	// Flight Rules (always visible, most important filter)
	imgui.Text("Flight Rules:")
	imgui.SameLine()
	flightRulesInt := int32(c.weatherFilter.FlightRules)
	if imgui.RadioButtonIntPtr("Any##fr", &flightRulesInt, int32(wx.FlightRulesAny)) {
		c.weatherFilter.FlightRules = wx.FlightRulesAny
		changed = true
	}
	imgui.SameLine()
	if imgui.RadioButtonIntPtr("VMC##fr", &flightRulesInt, int32(wx.FlightRulesVMC)) {
		c.weatherFilter.FlightRules = wx.FlightRulesVMC
		changed = true
	}
	imgui.SameLine()
	if imgui.RadioButtonIntPtr("IMC##fr", &flightRulesInt, int32(wx.FlightRulesIMC)) {
		c.weatherFilter.FlightRules = wx.FlightRulesIMC
		changed = true
	}

	// Temperature
	imgui.Text("Temperature (C):")
	imgui.SameLine()
	if optionalIntInput("##tempMin", "Min", &c.weatherFilter.TemperatureMin) {
		changed = true
	}
	imgui.SameLine()
	imgui.Text("-")
	imgui.SameLine()
	if optionalIntInput("##tempMax", "Max", &c.weatherFilter.TemperatureMax) {
		changed = true
	}

	// Surface Wind group
	imgui.SeparatorText("Surface Wind")
	if imgui.BeginTableV("surfaceWind", 2, imgui.TableFlagsSizingFixedFit, imgui.Vec2{}, 0) {
		imgui.TableSetupColumnV("Label", imgui.TableColumnFlagsWidthFixed, 100, 0)
		imgui.TableSetupColumnV("Value", imgui.TableColumnFlagsWidthStretch, 0, 0)

		// Direction (most important for runway selection)
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text("Direction (mag):")
		imgui.TableNextColumn()
		if optionalIntInput("##windDirMin", "Min", &c.weatherFilter.WindDirMin) {
			changed = true
		}
		imgui.SameLine()
		imgui.Text("-")
		imgui.SameLine()
		if optionalIntInput("##windDirMax", "Max", &c.weatherFilter.WindDirMax) {
			changed = true
		}

		// Speed
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text("Speed (kt):")
		imgui.TableNextColumn()
		if optionalIntInput("##windSpeedMin", "Min", &c.weatherFilter.WindSpeedMin) {
			changed = true
		}
		imgui.SameLine()
		imgui.Text("-")
		imgui.SameLine()
		if optionalIntInput("##windSpeedMax", "Max", &c.weatherFilter.WindSpeedMax) {
			changed = true
		}

		// Gusting
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text("Gusting:")
		imgui.TableNextColumn()
		gustInt := int32(c.weatherFilter.Gusting)
		if imgui.RadioButtonIntPtr("Any##gust", &gustInt, int32(wx.GustAny)) {
			c.weatherFilter.Gusting = wx.GustAny
			changed = true
		}
		imgui.SameLine()
		if imgui.RadioButtonIntPtr("Yes##gust", &gustInt, int32(wx.GustYes)) {
			c.weatherFilter.Gusting = wx.GustYes
			changed = true
		}
		imgui.SameLine()
		if imgui.RadioButtonIntPtr("No##gust", &gustInt, int32(wx.GustNo)) {
			c.weatherFilter.Gusting = wx.GustNo
			changed = true
		}

		imgui.EndTable()
	}

	// Winds Aloft group (only show if atmosByTime is available)
	if c.atmosByTime != nil {
		altLabel := fmt.Sprintf("Winds Aloft (%s)", av.FormatAltitude(c.windsAloftAltitude))
		imgui.SeparatorText(altLabel)
		if imgui.BeginTableV("windsAloft", 2, imgui.TableFlagsSizingFixedFit, imgui.Vec2{}, 0) {
			imgui.TableSetupColumnV("Label", imgui.TableColumnFlagsWidthFixed, 100, 0)
			imgui.TableSetupColumnV("Value", imgui.TableColumnFlagsWidthStretch, 0, 0)

			// Direction
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Direction (mag):")
			imgui.TableNextColumn()
			aloftDirChanged := optionalIntInput("##aloftDirMin", "Min", &c.weatherFilter.WindsAloftDirMin)
			imgui.SameLine()
			imgui.Text("-")
			imgui.SameLine()
			aloftDirChanged = optionalIntInput("##aloftDirMax", "Max", &c.weatherFilter.WindsAloftDirMax) || aloftDirChanged
			// Only trigger update when both values are set or both are empty
			if aloftDirChanged {
				bothSet := c.weatherFilter.WindsAloftDirMin != nil && c.weatherFilter.WindsAloftDirMax != nil
				bothEmpty := c.weatherFilter.WindsAloftDirMin == nil && c.weatherFilter.WindsAloftDirMax == nil
				if bothSet || bothEmpty {
					changed = true
				}
			}

			// Speed
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Speed (kt):")
			imgui.TableNextColumn()
			if optionalIntInput("##aloftSpeedMin", "Min", &c.weatherFilter.WindsAloftSpeedMin) {
				changed = true
			}
			imgui.SameLine()
			imgui.Text("-")
			imgui.SameLine()
			if optionalIntInput("##aloftSpeedMax", "Max", &c.weatherFilter.WindsAloftSpeedMax) {
				changed = true
			}

			imgui.EndTable()
		}
	}

	// Filter error (if any)
	if c.weatherFilterError != "" {
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .5, .5, 1})
		imgui.Text(renderer.FontAwesomeIconExclamationTriangle + " " + c.weatherFilterError)
		imgui.PopStyleColor()
	}

	imgui.Separator()

	// Start time and METAR section
	metarAirports := slices.Collect(maps.Keys(c.airportMETAR))
	slices.Sort(metarAirports)
	if idx := slices.Index(metarAirports, c.ScenarioSpec.PrimaryAirport); idx > 0 {
		metarAirports = slices.Delete(metarAirports, idx, idx+1)
		metarAirports = slices.Insert(metarAirports, 0, c.ScenarioSpec.PrimaryAirport)
	}

	if imgui.BeginTableV("timeAndMetar", 2, imgui.TableFlagsSizingFixedFit, imgui.Vec2{}, 0) {
		imgui.TableSetupColumnV("Label", imgui.TableColumnFlagsWidthFixed, 70, 0)
		imgui.TableSetupColumnV("Value", imgui.TableColumnFlagsWidthStretch, 0, 0)

		// Start time
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text("Start time:")
		imgui.TableNextColumn()
		metar := c.airportMETAR[metarAirports[0]]
		TimePicker(&c.NewSimRequest.StartTime, c.availableWXIntervals, metar, ui.fixedFont)
		imgui.SameLine()
		if imgui.Button(renderer.FontAwesomeIconRedo + "##refreshTime") {
			c.updateStartTimeForRunways()
		}
		if imgui.IsItemHovered() {
			imgui.SetTooltip("Select random time matching weather filters")
		}

		// METAR
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text("METAR:")
		imgui.TableNextColumn()
		currentMetar := wx.METARForTime(c.airportMETAR[metarAirports[0]], c.NewSimRequest.StartTime)
		ui.fixedFont.ImguiPush()
		imgui.Text(strings.TrimPrefix(strings.TrimPrefix(currentMetar.Raw, "METAR "), "SPECI "))
		imgui.PopFont()

		if c.showAllMETAR && len(metarAirports) > 1 {
			for i := 1; i < len(metarAirports); i++ {
				ap := metarAirports[i]
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.TableNextColumn()
				ui.fixedFont.ImguiPush()
				m := wx.METARForTime(c.airportMETAR[ap], c.NewSimRequest.StartTime)
				imgui.Text(strings.TrimPrefix(strings.TrimPrefix(m.Raw, "METAR "), "SPECI "))
				imgui.PopFont()
			}
		}

		imgui.EndTable()
	}

	if len(metarAirports) > 1 && !c.showAllMETAR {
		if imgui.Button("Show all airport METAR") {
			c.showAllMETAR = true
		}
	}

	if changed {
		c.updateStartTimeForRunways()
	}
}

func (c *NewSimConfiguration) updateStartTimeForRunways() {
	c.weatherFilterError = ""

	if c.ScenarioSpec == nil || c.airportMETAR == nil {
		return
	}

	airports := c.ScenarioSpec.AllAirports()
	if len(airports) == 0 {
		return
	}
	var ap string
	if slices.Contains(airports, c.ScenarioSpec.PrimaryAirport) {
		ap = c.ScenarioSpec.PrimaryAirport
	} else {
		ap = airports[0]
	}

	if apMETAR, ok := c.airportMETAR[ap]; ok && len(apMETAR) > 0 {
		// Sample using the combined weather filter (ground winds + winds aloft)
		sampledMETAR := wx.SampleWeatherWithFilter(
			apMETAR,
			c.atmosByTime,
			c.availableWXIntervals,
			&c.weatherFilter,
			c.windsAloftAltitude,
			c.ScenarioSpec.MagneticVariation)

		if sampledMETAR != nil {
			// Start at a random time between the sampled METAR and the next one
			startTime := sampledMETAR.Time.UTC()
			idx, _ := slices.BinarySearchFunc(apMETAR, sampledMETAR.Time, func(m wx.METAR, t time.Time) int {
				return m.Time.Compare(t)
			})
			if idx+1 < len(apMETAR) {
				validDuration := apMETAR[idx+1].Time.Sub(sampledMETAR.Time)
				startTime = startTime.Add(time.Duration(float64(validDuration) * float64(rand.Make().Float32())))
			}
			c.StartTime = startTime

			// Set VFR launch rate to zero if selected weather is IMC;
			// restore the original value if VMC.
			if !sampledMETAR.IsVMC() {
				c.ScenarioSpec.LaunchConfig.VFRDepartureRateScale = 0
			} else {
				c.ScenarioSpec.LaunchConfig.VFRDepartureRateScale = c.savedVFRDepartureRateScale
			}
		} else {
			c.weatherFilterError = "No weather matching filters found"
		}
	}
}

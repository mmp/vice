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
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/gui"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/scenario"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/traffic"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/AllenDang/cimgui-go/imgui"
)

type NewSimConfiguration struct {
	server.NewSimRequest

	selectedFacilityCatalogs map[string]*scenario.Catalog

	displayError error

	mgr             *client.ConnectionManager
	selectedServer  *client.Server
	defaultFacility *string
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

	// mu protects all fields written by the fetchMETAR and fetchTrafficPreview
	// goroutines and the inputs they read back when committing: the cached
	// METAR + atmospheric state below, the traffic preview counts at the end,
	// plus c.weatherFilter / c.weatherFilterError / c.StartTime /
	// c.savedVFRDepartureRateScale, and the c.GroupName / c.ScenarioSpec /
	// c.ScenarioName fields written by SetScenario. fetchSeq is incremented
	// each time SetScenario launches a new fetch; a goroutine bails if its
	// captured seq no longer matches, and the preview does the same with its
	// pending key.
	mu              util.LoggingMutex
	fetchSeq        uint64
	airportMETAR    map[av.ICAOAirportCode][]wx.METAR
	metarAirports   []av.ICAOAirportCode
	metarFacility   string
	metarWidth      int // characters; see metarText
	fetchMETARError error

	// Winds aloft data for the current facility
	atmosByTime         *wx.AtmosByTime
	windsAloftAltitudes [2]float32 // altitudes for WindsAloft[0] and [1]; 0 means unused
	isTRACON            bool

	availableWXIntervals []util.TimeInterval

	savedVFRDepartureRateScale float32

	// Traffic preview counts from the server, drawn under the traffic source
	// radio buttons. trafficPreviewKey identifies the settings the counts in
	// hand answer for and trafficPreviewPending the settings the request in
	// flight asks about; at most one request is out at a time, so dragging the
	// start time around coalesces to one round trip after another rather than a
	// pile of them.
	trafficPreviewKey        string
	trafficPreviewPending    string
	trafficPreviewDepartures []uint16
	trafficPreviewArrivals   []uint16
	// trafficPreviewOperations is the preview's traffic totaled by airport.
	// nil means no counts have arrived since the scenario or traffic source
	// was selected--never that a window was quiet--and it keeps the last
	// answer while a refetch for a scrubbed start time is in flight.
	trafficPreviewOperations map[av.ICAOAirportCode]int
	trafficPreviewError      error
	trafficPreviewRetryAt    time.Time
}

func MakeNewSimConfiguration(mgr *client.ConnectionManager, defaultFacility *string, lg *log.Logger) *NewSimConfiguration {
	c := &NewSimConfiguration{
		lg:              lg,
		mgr:             mgr,
		selectedServer:  mgr.LocalServer,
		defaultFacility: defaultFacility,
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
	var scenarioCatalog *scenario.Catalog
	c.GroupName, scenarioCatalog = util.FirstSortedMapEntry(c.selectedFacilityCatalogs)

	c.SetScenario(c.GroupName, scenarioCatalog.DefaultScenario)
}

func (c *NewSimConfiguration) SetScenario(groupName, scenarioName string) {
	var ok bool
	var scenarioCatalog *scenario.Catalog
	if scenarioCatalog, ok = c.selectedFacilityCatalogs[groupName]; !ok {
		c.lg.Errorf("%s: group not found in TRACON %s", groupName, c.Facility)
		groupName, scenarioCatalog = util.FirstSortedMapEntry(c.selectedFacilityCatalogs)
	}

	spec, ok := scenarioCatalog.Scenarios[scenarioName]
	if !ok {
		if scenarioName != "" {
			c.lg.Errorf("%s: scenario not found in group %s", scenarioName, groupName)
		}
		scenarioName = scenarioCatalog.DefaultScenario
		spec = scenarioCatalog.Scenarios[scenarioName]
	}

	airports := spec.AllAirports()
	facility := c.Facility

	c.mu.Lock(c.lg)
	c.GroupName = groupName
	c.ScenarioSpec = spec
	c.ScenarioName = scenarioName
	normalizeTrafficSourceConfig(c.ScenarioSpec)
	c.savedVFRDepartureRateScale = spec.LaunchConfig.VFRDepartureRateScale
	c.initDefaultWindDirection()
	c.clearTrafficPreviewLocked()
	c.fetchSeq++
	seq := c.fetchSeq
	if c.metarFacility != facility || !slices.Equal(c.metarAirports, airports) {
		c.clearWeatherLocked()
	}
	c.mu.Unlock(c.lg)

	go c.fetchMETAR(seq, facility, airports, spec)
}

func normalizeTrafficSourceConfig(spec *scenario.Spec) {
	lc := &spec.LaunchConfig
	lc.TimetableStartMinute = min(max(lc.TimetableStartMinute, 0), 24*60-1)
	lc.PublishedArrivalRateScale = math.Clamp(lc.PublishedArrivalRateScale, 0, sim.MaxPublishedRateScale)
	lc.PublishedDepartureRateScale = math.Clamp(lc.PublishedDepartureRateScale, 0, sim.MaxPublishedRateScale)

	// The server decides which sources a scenario can be flown with; fall back
	// to the first it offers rather than one it would refuse.
	if !slices.Contains(spec.TrafficSources, lc.TrafficSource) && len(spec.TrafficSources) > 0 {
		lc.TrafficSource = spec.TrafficSources[0]
	}

	if len(spec.Timetables) == 0 {
		lc.TimetableID, lc.TimetableAirport = "", ""
		return
	}

	for _, timetable := range spec.Timetables {
		if timetable.ID == lc.TimetableID && timetable.Airport == lc.TimetableAirport {
			return
		}
	}
	lc.TimetableID, lc.TimetableAirport = spec.Timetables[0].ID, spec.Timetables[0].Airport
}

// A scenario may offer timetables for more than one of its airports, so it
// takes both the id and the airport to name one.
func selectedTimetableSummary(spec *scenario.Spec) (traffic.TimetableSummary, bool) {
	for _, timetable := range spec.Timetables {
		if timetable.ID == spec.LaunchConfig.TimetableID &&
			timetable.Airport == spec.LaunchConfig.TimetableAirport {
			return timetable, true
		}
	}
	return traffic.TimetableSummary{}, false
}

// timetableLabel names a timetable in the picker. Timetables at different
// airports may share a name, so the airport goes in the label when the
// scenario offers more than one airport's.
func timetableLabel(spec *scenario.Spec, timetable traffic.TimetableSummary) string {
	for _, other := range spec.Timetables {
		if other.Airport != timetable.Airport {
			return db.AirportDisplayId(timetable.Airport) + " " + timetable.Name
		}
	}
	return timetable.Name
}

func (c *NewSimConfiguration) trafficSourceTooltip(source sim.TrafficSource, spec *scenario.Spec) string {
	switch source {
	case sim.TrafficSourceScenario:
		return "Traffic generated from the scenario's own definitions, at the arrival\n" +
			"and departure rates you set."
	case sim.TrafficSourceTimetable:
		return "Fly a curated daily timetable, starting at the selected time.\n" +
			"Overflights remain randomly generated."
	case sim.TrafficSourceHistorical:
		return "Fly the traffic that really operated at " + c.NewSimRequest.Facility +
			" on the selected date,\nfrom recorded flight data."
	default:
		return ""
	}
}

func (c *NewSimConfiguration) drawTrafficSourceUI(spec *scenario.Spec, p platform.Platform) {
	lc := &spec.LaunchConfig

	// Only the sources the server says it will run this scenario with are
	// offered: a scenario that gives no airlines has no traffic of its own to
	// generate, and most facilities have no timetable.
	imgui.Text("IFR traffic source:")
	prevSource := lc.TrafficSource
	for _, source := range spec.TrafficSources {
		imgui.SameLine()
		if len(spec.TrafficSources) == 1 {
			imgui.Text(source.String())
		} else {
			imgui.RadioButtonIntPtr(source.String(), (*int32)(&lc.TrafficSource), int32(source))
		}
		if imgui.IsItemHovered() {
			imgui.SetTooltip(c.trafficSourceTooltip(source, spec))
		}
	}
	if lc.TrafficSource != prevSource {
		// Counts from one source must never rank another's airports.
		c.clearTrafficPreviewLocked()
	}

	normalizeTrafficSourceConfig(spec)

	if lc.TrafficSource == sim.TrafficSourceTimetable {
		selected, _ := selectedTimetableSummary(spec)

		imgui.Text("Timetable:")
		imgui.SameLine()
		imgui.SetNextItemWidth(260)
		if imgui.BeginCombo("##timetable", timetableLabel(spec, selected)) {
			for _, timetable := range spec.Timetables {
				isSelected := timetable.ID == lc.TimetableID && timetable.Airport == lc.TimetableAirport
				if imgui.SelectableBoolV(timetableLabel(spec, timetable), isSelected, 0, imgui.Vec2{}) {
					lc.TimetableID, lc.TimetableAirport = timetable.ID, timetable.Airport
				}
				if isSelected {
					imgui.SetItemDefaultFocus()
				}
			}
			imgui.EndCombo()
		}
	}

	// Scenario traffic comes at the rates set below, which the Departures and
	// Arrivals headers already total; only published traffic has a shape worth
	// plotting.
	if lc.TrafficSource != sim.TrafficSourceScenario {
		c.drawTrafficPlot(spec, p)
	}
}

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
func getARTCCForFacility(facility string, catalog *scenario.Catalog) string {
	if catalog != nil && catalog.ARTCC != "" {
		return catalog.ARTCC
	}
	if artcc := db.DB.ARTCCForFacility(facility); artcc != "" {
		return artcc
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
	if traconInfo, ok := db.DB.TRACONs[facility]; ok {
		name := trimFacilityName(traconInfo.Name, "TRACON")
		return util.Select(name == "", facility, fmt.Sprintf("%s (%s)", facility, name))
	}
	if atctInfo, ok := db.DB.ATCTs[facility]; ok {
		name := trimFacilityName(atctInfo.Name, "ATCT")
		return util.Select(name == "", facility, fmt.Sprintf("%s (%s)", facility, name))
	}
	if artccInfo, ok := db.DB.ARTCCs[facility]; ok {
		name := trimFacilityName(artccInfo.Name, "ARTCC")
		return util.Select(name == "", facility, fmt.Sprintf("%s (%s)", facility, name))
	}
	return facility
}

// getAreaKey returns the area identifier for grouping scenarios.
// For TRACONs, returns the groupName; for ARTCCs, returns the trimmed Area field.
func getAreaKey(facility, groupName string, catalog *scenario.Catalog) string {
	if db.DB.IsTRACON(facility) {
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
		spec         *scenario.Spec
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
		catalogHasMatchingAirport := func(catalog *scenario.Catalog) bool {
			return filterLower == "" || util.SeqContainsFunc(slices.Values(catalog.Airports),
				func(ap av.ICAOAirportCode) bool { return strings.Contains(strings.ToLower(string(ap)), filterLower) })
		}

		// Helper to check if a catalog has matching scenario names
		catalogHasMatchingScenario := func(catalog *scenario.Catalog) bool {
			return filterLower == "" || util.SeqContainsFunc(maps.Keys(catalog.Scenarios),
				func(scenarioName string) bool { return strings.Contains(strings.ToLower(scenarioName), filterLower) })
		}

		// Helper to check if a catalog matches the filter (name, facility, airports, or scenarios)
		catalogMatchesFilter := func(catalog *scenario.Catalog) bool {
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
		facilityMatchesFilter := func(facility string, catalogs map[string]*scenario.Catalog) bool {
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
			facilityCatalogs := make(map[string]*scenario.Catalog, len(catalogsByFacility))
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
				if artccInfo, ok := db.DB.ARTCCs[artcc]; ok {
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
					name := trimFacilityName(db.DB.ARTCCs[artcc].Name, "ARTCC")
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
					isTRACON := db.DB.IsTRACON(facility)
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
						for aInfo := range util.SortedMapValues(areaToGroups) {
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
						catalog *scenario.Catalog
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
				a = append(a, string(rwy.Airport)+"/"+string(rwy.Runway))
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
		if desc := c.ScenarioSpec.Description; desc != "" {
			imgui.Spacing()
			imgui.Separator()
			imgui.Spacing()
			if imgui.BeginChildStrV("scenario_desc", imgui.Vec2{0, 0}, imgui.ChildFlagsBorders|imgui.ChildFlagsAutoResizeY, 0) {
				imgui.TextWrapped(desc)
			}
			imgui.EndChild()
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
					imgui.Text(gui.Icons.Lock)
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
			var result strings.Builder
			result.WriteString(controllerDisplayLabel(controllersForGroup, av.ControlPosition(cons.PrimaryTCP)))
			for _, sec := range cons.SecondaryTCPs {
				prefix := ""
				if sec.Type == sim.ConsolidationBasic {
					prefix = "*"
				}
				result.WriteString(" " + prefix +
					controllerDisplayLabel(controllersForGroup, av.ControlPosition(sec.TCP)))
			}
			return result.String()
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
			const tcwsPerRow = 8
			tcwCol := 0
			startX := imgui.CursorPosX()
			style := imgui.CurrentStyle()
			// Measure column width from the radio button circle plus the widest
			// 2-character label, then add spacing.
			colWidth := imgui.FrameHeight() + style.ItemInnerSpacing().X +
				imgui.CalcTextSizeV("WW", false, 0).X + style.ItemSpacing().X
			for tcw, cons := range util.SortedMap(rs.CurrentConsolidation) {
				// Filter: relief shows only occupied, normal shows only unoccupied
				if c.showReliefPositions != cons.IsOccupied() {
					continue
				}
				// Skip internal positions
				if len(tcw) > 0 && tcw[0] == '_' {
					continue
				}

				if tcwCol%tcwsPerRow != 0 {
					imgui.SameLine()
					imgui.SetCursorPosX(startX + float32(tcwCol%tcwsPerRow)*colWidth)
				}
				tcwCol++

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
				tcpCol := 0
				tcpStartX := imgui.CursorPosX()
				tcpStyle := imgui.CurrentStyle()
				tcpColWidth := imgui.FrameHeight() + tcpStyle.ItemInnerSpacing().X +
					imgui.CalcTextSizeV("WW", false, 0).X + tcpStyle.ItemSpacing().X
				for tcp := range util.SortedMap(availableTCPs) {
					if len(tcp) > 0 && tcp[0] == '_' {
						continue
					}

					if tcpCol%tcwsPerRow != 0 {
						imgui.SameLine()
						imgui.SetCursorPosX(tcpStartX + float32(tcpCol%tcwsPerRow)*tcpColWidth)
					}
					tcpCol++

					isSelected := c.selectedTCPs[tcp]
					label := controllerDisplayLabel(controllersForGroup, av.ControlPosition(tcp))
					if imgui.Checkbox(fmt.Sprintf("%s##tcp-%s", label, tcp), &isSelected) {
						if c.selectedTCPs == nil {
							c.selectedTCPs = make(map[sim.TCP]bool)
						}
						c.selectedTCPs[tcp] = isSelected
					}
				}

				if tcpCol%tcwsPerRow != 0 {
					imgui.SameLine()
					imgui.SetCursorPosX(tcpStartX + float32(tcpCol%tcwsPerRow)*tcpColWidth)
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
				imgui.Text(gui.Icons.ExclamationTriangle + " Must enter initials")
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
		imgui.Text(gui.Icons.ExclamationTriangle + " Must enter initials")
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
				imgui.Text(gui.Icons.ExclamationTriangle)
				imgui.PopStyleColor()
			}
		}
		imgui.Spacing()
	}

	// SIMULATION SETTINGS section
	drawSectionHeader("Simulation Settings")

	// A published flight's callsign is the one it really used, so a clash can't
	// be settled by drawing another: the flight would be thrown away instead.
	// Only the scenario's own generator can resample, so the setting is offered
	// only there.
	publishedTraffic := c.ScenarioSpec != nil &&
		c.ScenarioSpec.LaunchConfig.TrafficSource != sim.TrafficSourceScenario
	if publishedTraffic {
		imgui.BeginDisabled()
		c.NewSimRequest.EnforceUniqueCallsignSuffix = false
	}
	imgui.Checkbox("Ensure unique callsign suffixes", &c.NewSimRequest.EnforceUniqueCallsignSuffix)
	if publishedTraffic {
		imgui.EndDisabled()
		imgui.SameLine()
		imgui.Text("(" + c.ScenarioSpec.LaunchConfig.TrafficSource.String() +
			" traffic flies the callsigns it really used)")
	}

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

	// TRAFFIC SOURCE section
	drawSectionHeader("Traffic Source")
	c.drawTrafficSourceUI(c.ScenarioSpec, p)
	imgui.Spacing()

	// TRAFFIC RATES section
	drawSectionHeader("Traffic Rates")

	// Rate limit warning
	const rateLimit = 100.0
	if !c.ScenarioSpec.LaunchConfig.CheckRateLimits(rateLimit) {
		c.ScenarioSpec.LaunchConfig.ClampRates(rateLimit)
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .5, .5, 1})
		imgui.Text(gui.Icons.ExclamationTriangle + " Rates reduced to stay within limits")
		imgui.PopStyleColor()
	}

	// Published IFR traffic or the scenario's own rate controls; drawDepartureUI
	// and drawArrivalUI show the appropriate controls for the traffic source.
	// The rate-derived totals mean nothing when the data decides the traffic, so
	// leave them out of the headers then. Only the traffic the user works is
	// theirs to configure: a scenario that flies a neighboring airport's
	// operations for realism isn't offering them to anybody, so a section with
	// nothing but that in it doesn't appear at all.
	lc := &c.ScenarioSpec.LaunchConfig
	if lc.HaveWorkedDepartures() {
		headerText := "Departures###departures"
		if lc.TrafficSource == sim.TrafficSourceScenario {
			headerText = fmt.Sprintf("Departures (Total: %d/hr)###departures",
				int(lc.WorkedDepartureRate()+0.5))
		}
		if imgui.CollapsingHeaderBoolPtr(headerText, nil) {
			drawDepartureUI(lc, p)
			imgui.Spacing()
		}
	}

	if lc.HaveWorkedArrivals() {
		headerText := "Arrivals###arrivals"
		if lc.TrafficSource == sim.TrafficSourceScenario {
			headerText = fmt.Sprintf("Arrivals (Total: %d/hr)###arrivals",
				int(lc.WorkedArrivalRate()+0.5))
		}
		if imgui.CollapsingHeaderBoolPtr(headerText, nil) {
			drawArrivalUI(lc, p)
			imgui.Spacing()
		}
	}

	// VFR Departures remain independent of the IFR traffic source.
	if len(lc.VFRAirportRates) > 0 {
		var vfrRate float32
		for _, rate := range lc.VFRAirportRates {
			r := rate * lc.VFRDepartureRateScale
			if r > 0 {
				vfrRate += r
			}
		}
		headerText := fmt.Sprintf(
			"VFR Departures (%d/hr)###vfrdepartures",
			int(vfrRate+0.5),
		)
		if imgui.CollapsingHeaderBoolPtr(headerText, nil) {
			drawVFRDepartureUI(lc, p)
			imgui.Spacing()
		}
	}

	// Overflights (collapsible)
	if lc.HaveWorkedOverflights() {
		ofRate := lc.WorkedOverflightRate()
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

func (c *NewSimConfiguration) Start(config *Config) error {
	c.ScenarioSpec.LaunchConfig.EnableTowerGoArounds = config.EnableTowerGoArounds

	if c.ScenarioSpec.LaunchConfig.TrafficSource == sim.TrafficSourceTimetable {
		minutes, err := sim.TimetableStartMinute(c.NewSimRequest.StartTime, c.ScenarioSpec.LaunchConfig.TimetableAirport)
		if err != nil {
			return err
		}
		c.ScenarioSpec.LaunchConfig.TimetableStartMinute = minutes
	}

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

// validStartDays returns the local days a sim may start on: whole days on the
// scenario's clock covered by weather and, for historical traffic, by flight
// data with at least a day left to fly, so that a sim started near the end of
// a stretch still has traffic to work. A stretch with less than that in it is
// no use to anyone and drops out.
func (c *NewSimConfiguration) validStartDays(spec *scenario.Spec) []time.Time {
	if spec == nil {
		return nil
	}

	intervals := c.availableWXIntervals
	if spec.LaunchConfig.TrafficSource == sim.TrafficSourceHistorical {
		flights := util.MapSlice(spec.HistoricalFlightIntervals, func(iv util.TimeInterval) util.TimeInterval {
			return util.TimeInterval{iv[0], iv[1].Add(-24 * time.Hour)}
		})
		flights = util.FilterSliceInPlace(flights, func(iv util.TimeInterval) bool {
			return iv[0].Before(iv[1])
		})
		intervals = util.IntersectIntervals(intervals, flights)
	}

	return getValidFullDays(intervals, makeScenarioClock(spec))
}

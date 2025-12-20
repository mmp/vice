// launch.go
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

	mu              util.LoggingMutex // protects airportMETAR/fetchingMETAR/availableWXIntervals
	airportMETAR    map[string][]wx.METAR
	metarAirports   []string
	fetchMETARError error
	fetchingMETAR   bool
	metarGeneration int

	availableWXIntervals []util.TimeInterval
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

	c.fetchMETAR()

	c.updateStartTimeForRunways()
}

func (c *NewSimConfiguration) fetchMETAR() {
	c.mu.Lock(c.lg)
	defer c.mu.Unlock(c.lg)

	if c.ScenarioSpec == nil || c.selectedServer == nil {
		c.fetchingMETAR = false
		return
	}

	airports := c.ScenarioSpec.AllAirports()
	if slices.Equal(c.metarAirports, airports) {
		// No need to refetch
		return
	}

	c.airportMETAR = nil
	c.fetchMETARError = nil
	c.availableWXIntervals = nil
	c.fetchingMETAR = true
	c.metarGeneration++
	currentGeneration := c.metarGeneration

	c.mgr.GetMETAR(c.selectedServer, airports, func(metars map[string][]wx.METAR, err error) {
		c.mu.Lock(c.lg)
		defer c.mu.Unlock(c.lg)

		if currentGeneration != c.metarGeneration {
			return
		}

		c.fetchingMETAR = false

		if err != nil {
			c.fetchMETARError = err
		} else {
			c.airportMETAR = metars
			c.metarAirports = airports
			c.computeAvailableWXIntervals()
			c.updateStartTimeForRunways()
		}
	})
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

	// Get TRACON intervals from server
	var traconIntervals []util.TimeInterval
	if c.selectedServer != nil && c.selectedServer.AvailableWXByTRACON != nil {
		if intervals, ok := c.selectedServer.AvailableWXByTRACON[c.Facility]; ok {
			traconIntervals = intervals
		}
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

func (c *NewSimConfiguration) ShowRatesWindow() bool {
	return c.newSimType != NewSimJoinRemote
}

func (c *NewSimConfiguration) DrawUI(p platform.Platform, config *Config) bool {
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

	getARTCCForFacility := func(facility string, catalog *server.ScenarioCatalog) string {
		if catalog != nil && catalog.ARTCC != "" {
			return catalog.ARTCC
		}
		if traconInfo, ok := av.DB.TRACONs[facility]; ok {
			return traconInfo.ARTCC
		}
		return facility
	}

	trimFacilityName := func(name, facilityType string) string {
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

	formatFacilityLabel := func(facility string, catalog *server.ScenarioCatalog) string {
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

	drawGetControllerInitials := func() {
		imgui.Text("Controller Initials:")
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
	}

	if c.newSimType == NewSimCreateLocal || c.newSimType == NewSimCreateRemote {
		flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg |
			imgui.TableFlagsSizingStretchProp
		tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
		if imgui.BeginTableV("SelectScenario", 3, flags, imgui.Vec2{tableScale * 600, tableScale * 300}, 0.) {
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

			selectedARTCC := ""
			if c.Facility != "" {
				selectedARTCC = getARTCCForFacility(c.Facility, facilityCatalogs[c.Facility])
			}

			// Collect unique ARTCCs with matching scenarios
			artccs := make(map[string]interface{})
			for facility, info := range facilityCatalogs {
				if info != nil {
					artcc := getARTCCForFacility(facility, info)
					artccs[artcc] = nil
				}
			}

			// Column 1: ARTCC list
			imgui.TableNextColumn()
			if imgui.BeginChildStrV("artccs", imgui.Vec2{tableScale * 150, tableScale * 350}, 0, imgui.WindowFlagsNoResize) {
				for artcc := range util.SortedMap(artccs) {
					name := trimFacilityName(av.DB.ARTCCs[artcc].Name, "ARTCC")
					if name == "" {
						name = artcc
					}
					label := fmt.Sprintf("%s (%s)", artcc, name)
					if imgui.SelectableBoolV(label, artcc == selectedARTCC, 0, imgui.Vec2{}) && artcc != selectedARTCC {
						// Find first facility in this ARTCC
						idx := slices.IndexFunc(allFacilities, func(facility string) bool {
							return getARTCCForFacility(facility, facilityCatalogs[facility]) == artcc
						})
						if idx >= 0 {
							c.SetFacility(allFacilities[idx])
						}
					}
				}
			}
			imgui.EndChild()

			// Column 2: TRACONs or ARTCC areas for selected ARTCC
			imgui.TableNextColumn()
			if imgui.BeginChildStrV("tracons/areas", imgui.Vec2{tableScale * 150, tableScale * 350}, 0, imgui.WindowFlagsNoResize) {
				for _, facility := range allFacilities {
					info := facilityCatalogs[facility]
					if info == nil {
						continue
					}
					artcc := getARTCCForFacility(facility, info)
					if selectedARTCC != "" && artcc != selectedARTCC {
						continue
					}

					// Build area/group structure for this facility
					catalogs := catalogsByFacility[facility]
					_, isTRACON := av.DB.TRACONs[facility]
					areaToGroups := make(map[string]*areaInfo)

					for groupName, gcfg := range catalogs {
						// For TRACONs, use groupName as area; for ARTCCs, use the Area field
						area := groupName
						if !isTRACON {
							area = trimFacilityName(gcfg.Area, "Area")
						}
						if areaToGroups[area] == nil {
							areaToGroups[area] = &areaInfo{area: area}
						}
						areaToGroups[area].groupNames = append(areaToGroups[area].groupNames, groupName)
					}

					if len(areaToGroups) == 0 {
						continue
					}

					// Display facility label
					label := formatFacilityLabel(facility, info)
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
			if imgui.BeginChildStrV("scenarios", imgui.Vec2{tableScale * 300, tableScale * 350}, 0, imgui.WindowFlagsNoResize) {
				selectedCatalog := c.selectedFacilityCatalogs[c.GroupName]
				if selectedCatalog != nil {
					// Use same area logic as column 2: for TRACONs, use group name; for ARTCCs, use Area field
					_, isTRACON := av.DB.TRACONs[c.Facility]
					selectedArea := c.GroupName
					if !isTRACON {
						selectedArea = trimFacilityName(selectedCatalog.Area, "Area")
					}

					// Collect all scenarios from groups with the same area
					var allScenarios []scenarioInfo
					for groupName, group := range c.selectedFacilityCatalogs {
						groupArea := groupName
						if !isTRACON {
							groupArea = trimFacilityName(group.Area, "Area")
						}
						if groupArea == selectedArea {
							for name, spec := range group.Scenarios {
								allScenarios = append(allScenarios, scenarioInfo{
									groupName:    groupName,
									scenarioName: name,
									spec:         spec,
								})
							}
						}
					}

					// Sort and display scenarios
					sort.Slice(allScenarios, func(i, j int) bool {
						return allScenarios[i].scenarioName < allScenarios[j].scenarioName
					})
					for _, s := range allScenarios {
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

		if c.newSimType == NewSimCreateRemote {
			imgui.Text("Name: " + c.NewSimName)
		}

		fmtPosition := func(id sim.TCP) string {
			if catalog := c.selectedFacilityCatalogs[c.GroupName]; catalog != nil {
				if ctrl, ok := catalog.ControlPositions[id]; ok {
					return string(id) + " (" + ctrl.Position + ")"
				}
			}
			return string(id)
		}

		if len(c.ScenarioSpec.ArrivalRunways) > 0 {
			var a []string
			for _, rwy := range c.ScenarioSpec.ArrivalRunways {
				a = append(a, rwy.Airport+"/"+rwy.Runway)
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

		rootPosition, _ := c.ScenarioSpec.ControllerConfiguration.RootPosition()
		imgui.Text("Control Position: " + fmtPosition(rootPosition))

		drawGetControllerInitials()

		if c.newSimType == NewSimCreateRemote {
			// Various extras only for remote sims
			imgui.Checkbox("Sign in with instructor/RPO privileges", &c.Privileged)

			imgui.Checkbox("Require Password", &c.RequirePassword)
			if c.RequirePassword {
				imgui.InputTextWithHint("Password", "", &c.Password, 0, nil)
				if c.Password == "" {
					imgui.SameLine()
					imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{.7, .1, .1, 1})
					imgui.Text(renderer.FontAwesomeIconExclamationTriangle)
					imgui.PopStyleColor()
				}
			}
		}

		// Show TTS disable option if the server supports TTS
		if c.selectedServer.HaveTTS {
			imgui.Checkbox("Disable text-to-speech", &config.DisableTextToSpeech)
			// Also update configs for both joining remote sims and creating new sims
			c.joinRequest.DisableTextToSpeech = config.DisableTextToSpeech
			c.NewSimRequest.DisableTextToSpeech = config.DisableTextToSpeech
		}

		imgui.Checkbox("Ensure that the last two characters in callsigns are unique",
			&c.NewSimRequest.EnforceUniqueCallsignSuffix)

		imgui.SliderFloatV("Minimum interval between readback errors (minutes)", &c.PilotErrorInterval, 1, 30, "%.1f", imgui.SliderFlagsNone)

		// Lock for accessing METAR variables
		c.mu.Lock(c.lg)
		defer c.mu.Unlock(c.lg)

		if c.fetchingMETAR {
			imgui.Text("METAR: Fetching...")
		} else if c.fetchMETARError != nil {
			imgui.Text("METAR error: " + c.fetchMETARError.Error())
		} else if len(c.airportMETAR) > 0 {
			metarAirports := slices.Collect(maps.Keys(c.airportMETAR))
			slices.Sort(metarAirports)
			if idx := slices.Index(metarAirports, c.ScenarioSpec.PrimaryAirport); idx > 0 {
				// Move primary to the front
				metarAirports = slices.Delete(metarAirports, idx, idx+1)
				metarAirports = slices.Insert(metarAirports, 0, c.ScenarioSpec.PrimaryAirport)
			}

			metar := c.airportMETAR[metarAirports[0]]
			TimePicker("Simulation start date/time", &c.NewSimRequest.StartTime,
				c.availableWXIntervals, metar, &ui.fixedFont.Ifont)

			imgui.SameLine()
			if imgui.Button(renderer.FontAwesomeIconRedo) {
				c.updateStartTimeForRunways()
			}

			tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
			if imgui.BeginTableV("metar", 2, imgui.TableFlagsSizingFixedFit, imgui.Vec2{tableScale * 800, 0}, 0.) {
				// Determine how many METARs to show
				numToShow := len(metarAirports)
				if !c.showAllMETAR && len(metarAirports) > 5 {
					numToShow = 4
				}

				for i := range numToShow {
					ap := metarAirports[i]
					imgui.TableNextRow()
					imgui.TableNextColumn()
					if i == 0 {
						imgui.Text("METAR:")
					}
					imgui.TableNextColumn()
					imgui.PushFont(&ui.fixedFont.Ifont)
					metar := wx.METARForTime(c.airportMETAR[ap], c.NewSimRequest.StartTime)
					imgui.Text(metar.Raw)
					imgui.PopFont()
				}
				imgui.EndTable()
			}

			// Show button if there are more than 5 METARs and we haven't clicked to show all
			if !c.showAllMETAR && len(metarAirports) > 5 {
				if imgui.Button("Show all METAR") {
					c.showAllMETAR = true
				}
			}
		}
	} else {
		// Join remote
		rs, ok := runningSims[c.joinRequest.SimName]
		if !ok || c.joinRequest.SimName == "" {
			c.joinRequest.SimName, rs = util.FirstSortedMapEntry(runningSims)
		}

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
						occupiedTCWs = append(occupiedTCWs, string(tcw))
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
			result := string(cons.PrimaryTCP)
			for _, sec := range cons.SecondaryTCPs {
				prefix := ""
				if sec.Type == sim.ConsolidationBasic {
					prefix = "*"
				}
				result += " " + prefix + string(sec.TCP)
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

				selected := tcw == c.selectedTCW
				if imgui.RadioButtonBool(string(tcw)+"##tcw", selected) {
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
					if imgui.Checkbox(string(tcp)+"##tcp", &isSelected) {
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

func (c *NewSimConfiguration) OkDisabled(config *Config) bool {
	if len(config.ControllerInitials) != 2 {
		return true
	}
	if c.newSimType == NewSimJoinRemote && c.selectedTCW == "" {
		return true
	}
	return c.newSimType == NewSimCreateRemote && (c.NewSimName == "" || (c.RequirePassword && c.Password == ""))
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

	imgui.Text("Departures")

	sumRates := lc.TotalDepartureRate()
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

	imgui.Text(fmt.Sprintf("Overall departure rate: %d / hour", int(sumRates+0.5)))

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
				imgui.PushIDStr(runway)

				for category := range util.SortedMap(lc.DepartureRates[airport][runway]) {
					imgui.TableNextColumn()
					rshort, _, _ := strings.Cut(runway, ".") // don't include extras in the UI
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
						lc.DepartureRates[airport][runway][category] = float32(r) / lc.DepartureRateScale
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

	sumVFRRates := 0
	for _, rate := range lc.VFRAirportRates {
		r := float32(rate) * lc.VFRDepartureRateScale
		if r > 0 {
			sumVFRRates += int(r)
		}
	}
	imgui.Text(fmt.Sprintf("Overall VFR departure rate: %d / hour", sumVFRRates))
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
	sumRates := lc.TotalArrivalRate()
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

	imgui.Text("Arrivals")
	imgui.Text(fmt.Sprintf("Overall arrival rate: %d / hour", int(sumRates+0.5)))

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
						lc.InboundFlowRates[group][ap] = float32(r) / lc.InboundFlowRateScale
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
	overflightGroups := make(map[string]interface{})
	sumRates := lc.TotalOverflightRate()
	for group, rates := range lc.InboundFlowRates {
		if _, ok := rates["overflights"]; ok {
			overflightGroups[group] = nil
		}
	}
	if len(overflightGroups) == 0 {
		return
	}

	imgui.Text("Overflights")
	imgui.Text(fmt.Sprintf("Overall overflight rate: %d / hour", int(sumRates+0.5)))

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
				lc.InboundFlowRates[group]["overflights"] = float32(r) / lc.InboundFlowRateScale
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
		0, 20, "%.1f", imgui.SliderFlagsNone)
}

///////////////////////////////////////////////////////////////////////////

type LaunchControlWindow struct {
	client              *client.ControlClient
	departures          []*LaunchDeparture
	vfrDepartures       []*LaunchDeparture
	arrivalsOverflights []*LaunchArrivalOverflight
	lg                  *log.Logger
	selectedEmergency   int
}

type LaunchAircraft struct {
	Aircraft           sim.Aircraft
	Airport            string
	LastLaunchCallsign av.ADSBCallsign
	LastLaunchTime     time.Time
	TotalLaunches      int
}

func (la *LaunchAircraft) Reset() {
	la.LastLaunchCallsign = ""
	la.LastLaunchTime = time.Time{}
	la.TotalLaunches = 0
}

type LaunchDeparture struct {
	LaunchAircraft
	Runway   string
	Category string
}

type LaunchArrivalOverflight struct {
	LaunchAircraft
	Group string
}

func MakeLaunchControlWindow(client *client.ControlClient, lg *log.Logger) *LaunchControlWindow {
	lc := &LaunchControlWindow{client: client, lg: lg}

	config := &client.State.LaunchConfig
	for airport, runwayRates := range util.SortedMap(config.DepartureRates) {
		for rwy, rates := range util.SortedMap(runwayRates) {
			for category := range util.SortedMap(rates) {
				lc.departures = append(lc.departures, &LaunchDeparture{
					LaunchAircraft: LaunchAircraft{Airport: airport},
					Runway:         rwy,
					Category:       category,
				})
			}
		}
	}

	for airport := range util.SortedMap(config.VFRAirportRates) {
		rwy := client.State.VFRRunways[airport]
		lc.vfrDepartures = append(lc.vfrDepartures, &LaunchDeparture{
			LaunchAircraft: LaunchAircraft{Airport: airport},
			Runway:         rwy.Id,
		})
	}

	for group, apRates := range util.SortedMap(config.InboundFlowRates) {
		for ap := range apRates {
			lc.arrivalsOverflights = append(lc.arrivalsOverflights,
				&LaunchArrivalOverflight{
					LaunchAircraft: LaunchAircraft{Airport: ap},
					Group:          group,
				})
		}
	}

	if config.Mode == sim.LaunchManual {
		lc.spawnAllAircraft()
	}

	return lc
}

func (lc *LaunchControlWindow) spawnIFRDeparture(dep *LaunchDeparture) {
	lc.client.CreateDeparture(dep.Airport, dep.Runway, dep.Category, av.FlightRulesIFR, &dep.Aircraft,
		func(err error) {
			if err != nil {
				lc.lg.Warnf("CreateDeparture: %v", err)
			}
		})
}

func (lc *LaunchControlWindow) spawnVFRDeparture(dep *LaunchDeparture) {
	lc.client.CreateDeparture(dep.Airport, dep.Runway, dep.Category, av.FlightRulesVFR, &dep.Aircraft,
		func(err error) {
			if err != nil && server.TryDecodeError(err) != sim.ErrViolatedAirspace {
				lc.lg.Warnf("CreateDeparture: %v", err)
			}
		})
}

func (lc *LaunchControlWindow) spawnArrivalOverflight(lac *LaunchArrivalOverflight) {
	if lac.Airport != "overflights" {
		lc.client.CreateArrival(lac.Group, lac.Airport, &lac.Aircraft,
			func(err error) {
				if err != nil {
					lc.lg.Warnf("CreateArrival: %v", err)
				}
			})
	} else {
		lc.client.CreateOverflight(lac.Group, &lac.Aircraft,
			func(err error) {
				if err != nil {
					lc.lg.Warnf("CreateOverflight: %v", err)
				}
			})
	}
}

func (lc *LaunchControlWindow) getLastDeparture(airport, runway string) (callsign av.ADSBCallsign, launch time.Time) {
	match := func(dep *LaunchDeparture) bool {
		return dep.Airport == airport && dep.Runway == runway
	}
	if idx := slices.IndexFunc(lc.departures, match); idx != -1 {
		callsign, launch = lc.departures[idx].LastLaunchCallsign, lc.departures[idx].LastLaunchTime
	}
	if idx := slices.IndexFunc(lc.vfrDepartures, match); idx != -1 {
		if callsign == "" || lc.vfrDepartures[idx].LastLaunchTime.After(launch) {
			callsign, launch = lc.vfrDepartures[idx].LastLaunchCallsign, lc.vfrDepartures[idx].LastLaunchTime
		}
	}
	return
}

func (lc *LaunchControlWindow) spawnAllAircraft() {
	// Spawn all aircraft for automatic mode
	for i := range lc.departures {
		if lc.departures[i].Aircraft.ADSBCallsign == "" {
			lc.spawnIFRDeparture(lc.departures[i])
		}
	}
	for i := range lc.vfrDepartures {
		if lc.vfrDepartures[i].Aircraft.ADSBCallsign == "" {
			lc.spawnVFRDeparture(lc.vfrDepartures[i])
		}
	}
	for i := range lc.arrivalsOverflights {
		if lc.arrivalsOverflights[i].Aircraft.ADSBCallsign == "" {
			lc.spawnArrivalOverflight(lc.arrivalsOverflights[i])
		}
	}
}

func (lc *LaunchControlWindow) cleanupAircraft() {
	var toDelete []sim.Aircraft

	add := func(la *LaunchAircraft) {
		if la.Aircraft.ADSBCallsign != "" {
			toDelete = append(toDelete, la.Aircraft)
			la.Aircraft = sim.Aircraft{}
		}
	}

	for i := range lc.departures {
		add(&lc.departures[i].LaunchAircraft)
	}
	for i := range lc.vfrDepartures {
		add(&lc.vfrDepartures[i].LaunchAircraft)
	}
	for i := range lc.arrivalsOverflights {
		add(&lc.arrivalsOverflights[i].LaunchAircraft)
	}

	if len(toDelete) > 0 {
		lc.client.DeleteAircraft(toDelete, func(err error) {
			if err != nil {
				lc.lg.Errorf("Error deleting aircraft: %v", err)
			}
		})
	}
}

func (lc *LaunchControlWindow) Draw(eventStream *sim.EventStream, p platform.Platform) {
	showLaunchControls := true
	imgui.SetNextWindowSizeConstraints(imgui.Vec2{300, 100}, imgui.Vec2{-1, float32(p.WindowSize()[1]) * 19 / 20})
	imgui.BeginV("Launch Control", &showLaunchControls, imgui.WindowFlagsAlwaysAutoResize)

	ctrl := lc.client.State.LaunchConfig.Controller

	// Show launch control take/release buttons when there are multiple human controllers
	if len(lc.client.State.ActiveTCWs) > 1 {
		imgui.Text("Controlling controller: " + util.Select(ctrl == "", "(none)", string(ctrl)))
		if ctrl == lc.client.State.UserTCW {
			if imgui.Button("Release launch control") {
				lc.client.TakeOrReturnLaunchControl(eventStream)
			}
		} else {
			if imgui.Button("Take launch control") {
				lc.client.TakeOrReturnLaunchControl(eventStream)
			}
		}
	}

	canLaunch := ctrl == lc.client.State.UserTCW || (len(lc.client.State.ActiveTCWs) <= 1 && ctrl == "") ||
		lc.client.State.TCWIsPrivileged(lc.client.State.UserTCW)
	if canLaunch {
		imgui.Text("Mode:")
		imgui.SameLine()
		if imgui.RadioButtonIntPtr("Manual", &lc.client.State.LaunchConfig.Mode, sim.LaunchManual) {
			lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
			lc.spawnAllAircraft()
		}
		imgui.SameLine()
		if imgui.RadioButtonIntPtr("Automatic", &lc.client.State.LaunchConfig.Mode, sim.LaunchAutomatic) {
			lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
			lc.cleanupAircraft()
		}

		width, _ := ui.font.BoundText(renderer.FontAwesomeIconPlayCircle, 0)
		// Right-justify
		imgui.SameLine()
		imgui.Text("                            ")
		imgui.SameLine()
		//	imgui.SetCursorPos(imgui.Vec2{imgui.CursorPosX() + imgui.ContentRegionAvail().X - float32(3*width+10),
		imgui.SetCursorPos(imgui.Vec2{imgui.WindowWidth() - float32(7*width), imgui.CursorPosY()})
		if lc.client != nil && lc.client.Connected() {
			if lc.client.State.Paused {
				if imgui.Button(renderer.FontAwesomeIconPlayCircle) {
					lc.client.ToggleSimPause()
				}
				if imgui.IsItemHovered() {
					imgui.SetTooltip("Resume simulation")
				}
			} else {
				if imgui.Button(renderer.FontAwesomeIconPauseCircle) {
					lc.client.ToggleSimPause()
				}
				if imgui.IsItemHovered() {
					imgui.SetTooltip("Pause simulation")
				}
			}
		}

		imgui.SameLine()
		if imgui.Button(renderer.FontAwesomeIconTrash) {
			uiShowModalDialog(NewModalDialogBox(&YesOrNoModalClient{
				title: "Are you sure?",
				query: "All aircraft will be deleted. Go ahead?",
				ok: func() {
					lc.client.DeleteAllAircraft(nil)
					for _, dep := range lc.departures {
						dep.Reset()
					}
					for _, ac := range lc.arrivalsOverflights {
						ac.Reset()
					}
				},
			}, p), true)
		}
		if imgui.IsItemHovered() {
			imgui.SetTooltip("Delete all aircraft and restart")
		}

		flags := imgui.TableFlagsBordersH | imgui.TableFlagsBordersOuterV | imgui.TableFlagsRowBg |
			imgui.TableFlagsSizingStretchProp
		tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))

		if lc.client.State.LaunchConfig.Mode == sim.LaunchManual {
			mitAndTime := func(ac *sim.Aircraft, launchPosition math.Point2LL,
				lastLaunchCallsign av.ADSBCallsign, lastLaunchTime time.Time) {

				imgui.TableNextColumn()
				if prev, ok := lc.client.State.GetTrackByCallsign(lastLaunchCallsign); ok {
					dist := math.NMDistance2LL(prev.Location, launchPosition)
					imgui.Text(fmt.Sprintf("%.1f", dist))

					imgui.TableNextColumn()

					delta := lc.client.CurrentTime().Sub(lastLaunchTime).Round(time.Second).Seconds()
					m, s := int(delta)/60, int(delta)%60
					imgui.Text(fmt.Sprintf("%02d:%02d", m, s))
				} else {
					imgui.TableNextColumn()
				}
			}

			if imgui.CollapsingHeaderBoolPtr("Departures", nil) {
				ndep := util.ReduceSlice(lc.departures, func(dep *LaunchDeparture, n int) int {
					return n + dep.TotalLaunches
				}, 0)

				imgui.Text(fmt.Sprintf("Departures: %d total", ndep))

				// Sort departures by airport, then runway, then category
				sortedDeps := util.DuplicateSlice(lc.departures)
				slices.SortFunc(sortedDeps, func(a, b *LaunchDeparture) int {
					return strings.Compare(a.Airport+"/"+a.Runway+"/"+a.Category,
						b.Airport+"/"+b.Runway+"/"+b.Category)
				})

				// Find the maximum number of categories for any airport
				maxCategories, curCategories := 0, 1
				lastAp := ""
				for _, d := range sortedDeps {
					if d.Airport != lastAp {
						maxCategories = max(maxCategories, curCategories)
						curCategories = 1
						lastAp = d.Airport
					} else {
						curCategories++
					}
				}

				nColumns := min(3, maxCategories)
				if imgui.BeginTableV("dep", int32(1+9*nColumns), flags, imgui.Vec2{tableScale * float32(100+450*nColumns), 0}, 0.0) {
					imgui.TableSetupColumn("Airport")
					for range nColumns {
						imgui.TableSetupColumn("Rwy")
						imgui.TableSetupColumn("Category")
						imgui.TableSetupColumn("#")
						imgui.TableSetupColumn("Type")
						imgui.TableSetupColumn("Exit")
						imgui.TableSetupColumn("MIT")
						imgui.TableSetupColumn("Time")
						imgui.TableSetupColumn("")
						imgui.TableSetupColumn("")
					}
					imgui.TableHeadersRow()

					lastAp := ""
					curColumn := 0
					for _, dep := range sortedDeps {
						if dep.Airport != lastAp {
							imgui.TableNextRow()
							lastAp = dep.Airport
							curColumn = 0

							imgui.TableNextColumn()
							imgui.Text(dep.Airport)
						} else if curColumn+1 == nColumns {
							curColumn = 0
							imgui.TableNextRow()
							imgui.TableNextColumn()
						} else {
							curColumn++
						}

						imgui.TableNextColumn()
						rwy, _, _ := strings.Cut(dep.Runway, ".")
						imgui.Text(rwy)
						imgui.TableNextColumn()
						imgui.Text(dep.Category)

						imgui.PushIDStr(dep.Airport + " " + dep.Runway + " " + dep.Category)

						imgui.TableNextColumn()
						imgui.Text(strconv.Itoa(dep.TotalLaunches))

						if dep.Aircraft.ADSBCallsign != "" {
							imgui.TableNextColumn()
							imgui.Text(dep.Aircraft.FlightPlan.AircraftType)

							imgui.TableNextColumn()
							imgui.Text(dep.Aircraft.FlightPlan.Exit)

							lastCallsign, lastTime := lc.getLastDeparture(dep.Airport, dep.Runway)
							mitAndTime(&dep.Aircraft, dep.Aircraft.Position(), lastCallsign, lastTime)

							imgui.TableNextColumn()
							if imgui.Button(renderer.FontAwesomeIconPlaneDeparture) {
								lc.client.LaunchDeparture(dep.Aircraft, dep.Runway)
								dep.LastLaunchCallsign = dep.Aircraft.ADSBCallsign
								dep.LastLaunchTime = lc.client.CurrentTime()
								dep.TotalLaunches++

								dep.Aircraft = sim.Aircraft{}
								lc.spawnIFRDeparture(dep)
							}

							imgui.TableNextColumn()
							if imgui.Button(renderer.FontAwesomeIconRedo) {
								dep.Aircraft = sim.Aircraft{}
								lc.spawnIFRDeparture(dep)
							}
						} else {
							for range 7 {
								imgui.TableNextColumn()
							}
						}

						imgui.PopID()
					}

					imgui.EndTable()
				}
			}

			if len(lc.vfrDepartures) > 0 && imgui.CollapsingHeaderBoolPtr("VFR Departures", nil) {
				ndep := util.ReduceSlice(lc.vfrDepartures, func(dep *LaunchDeparture, n int) int {
					return n + dep.TotalLaunches
				}, 0)

				imgui.Text(fmt.Sprintf("VFR Departures: %d total", ndep))

				if imgui.Button("Request Flight Following") {
					lc.client.RequestFlightFollowing()
				}
				if imgui.IsItemHovered() {
					imgui.SetTooltip("Request VFR flight following from a random VFR aircraft")
				}

				nColumns := min(2, len(lc.vfrDepartures))
				if imgui.BeginTableV("vfrdep", int32(9*nColumns), flags, imgui.Vec2{tableScale * float32(100+450*nColumns), 0}, 0.0) {
					for range nColumns {
						imgui.TableSetupColumn("Airport")
						imgui.TableSetupColumn("Rwy")
						imgui.TableSetupColumn("#")
						imgui.TableSetupColumn("Dest.")
						imgui.TableSetupColumn("Type")
						imgui.TableSetupColumn("MIT")
						imgui.TableSetupColumn("Time")
						imgui.TableSetupColumn("")
						imgui.TableSetupColumn("")
					}
					imgui.TableHeadersRow()
					imgui.TableNextRow()

					for i, dep := range lc.vfrDepartures {
						if i%nColumns == 0 {
							imgui.TableNextRow()
						}

						imgui.PushIDStr(dep.Airport)
						imgui.TableNextColumn()
						imgui.Text(dep.Airport)
						imgui.TableNextColumn()
						imgui.Text(dep.Runway)
						imgui.TableNextColumn()
						imgui.Text(strconv.Itoa(dep.TotalLaunches))

						if dep.Aircraft.ADSBCallsign != "" {
							imgui.TableNextColumn()
							imgui.Text(dep.Aircraft.FlightPlan.ArrivalAirport)

							imgui.TableNextColumn()
							imgui.Text(dep.Aircraft.FlightPlan.AircraftType)

							lastCallsign, lastTime := lc.getLastDeparture(dep.Airport, dep.Runway)
							mitAndTime(&dep.Aircraft, dep.Aircraft.Position(), lastCallsign, lastTime)

							imgui.TableNextColumn()
							if imgui.Button(renderer.FontAwesomeIconPlaneDeparture) {
								lc.client.LaunchDeparture(dep.Aircraft, dep.Runway)
								dep.LastLaunchCallsign = dep.Aircraft.ADSBCallsign
								dep.LastLaunchTime = lc.client.CurrentTime()
								dep.TotalLaunches++

								dep.Aircraft = sim.Aircraft{}
								lc.spawnVFRDeparture(dep)
							}
						} else {
							// Since VFR routes are randomly sampled and then checked,
							// it may take a while to find a valid one; keep trying until
							// we get one.
							lc.spawnVFRDeparture(dep)
							for range 5 {
								imgui.TableNextColumn()
							}
						}
						imgui.TableNextColumn()
						if imgui.Button(renderer.FontAwesomeIconRedo) {
							dep.Aircraft = sim.Aircraft{}
							lc.spawnVFRDeparture(dep)
						}

						imgui.PopID()
					}

					imgui.EndTable()
				}
			}

			if imgui.CollapsingHeaderBoolPtr("Arrivals / Overflights", nil) {
				narof := util.ReduceSlice(lc.arrivalsOverflights, func(arr *LaunchArrivalOverflight, n int) int {
					return n + arr.TotalLaunches
				}, 0)

				imgui.Text(fmt.Sprintf("Arrivals/Overflights: %d total", narof))

				sortedInbound := util.DuplicateSlice(lc.arrivalsOverflights)
				slices.SortFunc(sortedInbound, func(a, b *LaunchArrivalOverflight) int {
					return strings.Compare(a.Airport+"/"+a.Group, b.Airport+"/"+b.Group)
				})

				maxGroups, numGroups := 0, 1
				lastAirport := ""
				for _, ao := range sortedInbound {
					if ao.Airport != lastAirport {
						maxGroups = max(maxGroups, numGroups)
						lastAirport = ao.Airport
						numGroups = 1
					} else {
						numGroups++
					}
				}
				numColumns := min(maxGroups, 3)

				if imgui.BeginTableV("arrof", int32(1+7*numColumns), flags, imgui.Vec2{tableScale * float32(100+350*numColumns), 0}, 0.0) {
					imgui.TableSetupColumn("Airport")
					for range numColumns {
						imgui.TableSetupColumn("Group")
						imgui.TableSetupColumn("#")
						imgui.TableSetupColumn("A/C Type")
						imgui.TableSetupColumn("MIT")
						imgui.TableSetupColumn("Time")
						imgui.TableSetupColumn("")
						imgui.TableSetupColumn("")
					}
					imgui.TableHeadersRow()

					curColumn := 0
					lastAirport := ""
					for _, arof := range sortedInbound {
						if arof.Airport != lastAirport {
							imgui.TableNextRow()
							lastAirport = arof.Airport
							curColumn = 0
							imgui.TableNextColumn()
							imgui.Text(arof.Airport)
						} else if curColumn+1 == numColumns {
							curColumn = 0
							imgui.TableNextRow()
							imgui.TableNextColumn()
							imgui.Text("")
						} else {
							curColumn++
						}

						imgui.PushIDStr(arof.Group + arof.Airport)

						imgui.TableNextColumn()
						imgui.Text(arof.Group)

						imgui.TableNextColumn()
						imgui.Text(strconv.Itoa(arof.TotalLaunches))

						if arof.Aircraft.ADSBCallsign != "" {
							imgui.TableNextColumn()
							imgui.Text(arof.Aircraft.FlightPlan.AircraftType)

							mitAndTime(&arof.Aircraft, arof.Aircraft.Position(), arof.LastLaunchCallsign,
								arof.LastLaunchTime)

							imgui.TableNextColumn()
							if imgui.Button(renderer.FontAwesomeIconPlaneDeparture) {
								lc.client.LaunchArrivalOverflight(arof.Aircraft)
								arof.LastLaunchCallsign = arof.Aircraft.ADSBCallsign
								arof.LastLaunchTime = lc.client.CurrentTime()
								arof.TotalLaunches++

								arof.Aircraft = sim.Aircraft{}
								lc.spawnArrivalOverflight(arof)
							}

							imgui.TableNextColumn()
							if imgui.Button(renderer.FontAwesomeIconRedo) {
								arof.Aircraft = sim.Aircraft{}
								lc.spawnArrivalOverflight(arof)
							}
						} else {
							for range 5 {
								imgui.TableNextColumn()
							}
						}

						imgui.PopID()
					}

					imgui.EndTable()
				}
			}
		} else {
			changed := false
			if imgui.CollapsingHeaderBoolPtr("Departures", nil) {
				changed = drawDepartureUI(&lc.client.State.LaunchConfig, p)
			}
			if len(lc.client.State.LaunchConfig.VFRAirportRates) > 0 &&
				imgui.CollapsingHeaderBoolPtr("VFR Departures", nil) {
				changed = drawVFRDepartureUI(&lc.client.State.LaunchConfig, p) || changed
			}
			if imgui.CollapsingHeaderBoolPtr("Arrivals / Overflights", nil) {
				changed = drawArrivalUI(&lc.client.State.LaunchConfig, p) || changed
				changed = drawOverflightUI(&lc.client.State.LaunchConfig, p) || changed
			}

			if changed {
				lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
			}
		}
	}

	if etypes := lc.client.State.Emergencies; len(etypes) > 0 {
		imgui.Text("Emergency: ")
		imgui.SameLine()

		emergencyLabel := func(et sim.Emergency) string {
			return et.Name + " (" + et.ApplicableTo.String() + ")"
		}
		imgui.SetNextItemWidth(300)
		if imgui.BeginCombo("##emergency", emergencyLabel(etypes[lc.selectedEmergency])) {
			for i, em := range etypes {
				if imgui.SelectableBoolV(emergencyLabel(em), i == lc.selectedEmergency, 0, imgui.Vec2{}) {
					lc.selectedEmergency = i
				}
			}
			imgui.EndCombo()
		}

		imgui.SameLine()
		if imgui.Button("Trigger") {
			lc.client.TriggerEmergency(etypes[lc.selectedEmergency].Name)
		}
	}

	flags := imgui.TableFlagsBordersH | imgui.TableFlagsBordersOuterV | imgui.TableFlagsRowBg |
		imgui.TableFlagsSizingStretchProp
	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))

	releaseAircraft := lc.client.State.GetRegularReleaseDepartures()
	if len(releaseAircraft) > 0 && imgui.CollapsingHeaderBoolPtr("Hold For Release", nil) {
		slices.SortFunc(releaseAircraft, func(a, b sim.ReleaseDeparture) int {
			// Just by airport, otherwise leave in FIFO order
			return strings.Compare(a.DepartureAirport, b.DepartureAirport)
		})

		if imgui.BeginTableV("Releases", 5, flags, imgui.Vec2{tableScale * 600, 0}, 0) {
			imgui.TableSetupColumn("Airport")
			imgui.TableSetupColumn("Callsign")
			imgui.TableSetupColumn("A/C Type")
			imgui.TableSetupColumn("Exit")
			// imgui.TableSetupColumn("#Release")
			imgui.TableHeadersRow()

			lastAp := ""
			for _, ac := range releaseAircraft {
				imgui.PushIDStr(string(ac.ADSBCallsign))
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(ac.DepartureAirport)
				imgui.TableNextColumn()
				imgui.Text(string(ac.ADSBCallsign))
				imgui.TableNextColumn()
				imgui.Text(ac.AircraftType)
				imgui.TableNextColumn()
				imgui.Text(ac.Exit)
				if ac.DepartureAirport != lastAp && !ac.Released {
					// Only allow releasing the first-up unreleased one.
					lastAp = ac.DepartureAirport
					imgui.TableNextColumn()
					if imgui.Button(renderer.FontAwesomeIconPlaneDeparture) {
						lc.client.ReleaseDeparture(ac.ADSBCallsign,
							func(err error) {
								if err != nil {
									lc.lg.Errorf("%s: %v", ac.ADSBCallsign, err)
								}
							})
					}
				}
				imgui.PopID()
			}

			imgui.EndTable()
		}
	}

	imgui.End()

	if !showLaunchControls {
		lc.client.TakeOrReturnLaunchControl(eventStream)
		lc.cleanupAircraft()
		ui.showLaunchControl = false
	}
}

func drawScenarioInfoWindow(config *Config, c *client.ControlClient, p platform.Platform, lg *log.Logger) bool {
	// Ensure that the window is wide enough to show the description
	sz := imgui.CalcTextSize(c.State.SimDescription)
	imgui.SetNextWindowSizeConstraints(imgui.Vec2{sz.X + 50, 0}, imgui.Vec2{100000, 100000})

	show := true
	imgui.BeginV(c.State.SimDescription, &show, imgui.WindowFlagsAlwaysAutoResize)

	if imgui.CollapsingHeaderBoolPtr("Controllers", nil) {
		// Make big(ish) tables somewhat more legible
		tableFlags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH |
			imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
		if imgui.BeginTableV("controllers", 3, tableFlags, imgui.Vec2{}, 0) {
			imgui.TableSetupColumn("Workstation")
			imgui.TableSetupColumn("Human")
			imgui.TableSetupColumn("Positions")
			imgui.TableHeadersRow()

			// First the potentially-human-controlled ones
			tcws := slices.Collect(maps.Keys(c.State.CurrentConsolidation))
			slices.Sort(tcws)
			coveredPositions := make(map[av.ControllerPosition]struct{})
			for _, tcw := range tcws {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(string(tcw))

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
						p = append(p, fmt.Sprintf("%s (%s, %s)", ctrl.PositionId(), ctrl.Position, ctrl.Frequency.String()))
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
				imgui.Text(string(ctrl.PositionId()))
				imgui.TableNextColumn()
				imgui.TableNextColumn()
				imgui.Text(fmt.Sprintf("%s (%s, %s)", ctrl.PositionId(), ctrl.Position, ctrl.Frequency.String()))
			}

			imgui.EndTable()
		}
	}

	config.DisplayRoot.VisitPanes(func(pane panes.Pane) {
		if draw, ok := pane.(panes.InfoWindowDrawer); ok {
			draw.DrawInfo(c, p, lg)
		}
	})
	imgui.End()

	return show
}

func (c *NewSimConfiguration) updateStartTimeForRunways() {
	if c.ScenarioSpec == nil || c.selectedServer == nil || c.airportMETAR == nil {
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

	// Use METAR sampling with wind specifier if available
	if apMETAR, ok := c.airportMETAR[ap]; ok && len(apMETAR) > 0 {
		var sampledMETAR *wx.METAR

		if c.ScenarioSpec.WindSpecifier != nil {
			// Use the scenario's wind specifier
			sampledMETAR = wx.SampleMETARWithSpec(apMETAR, c.availableWXIntervals,
				c.ScenarioSpec.WindSpecifier, c.ScenarioSpec.MagneticVariation)
		} else {
			// Fallback: calculate average runway heading and use that
			var sumRunwayVecs [2]float32
			if dbap, ok := av.DB.Airports[ap]; ok {
				for _, rwy := range dbap.Runways {
					if slices.ContainsFunc(c.ScenarioSpec.DepartureRunways, func(r sim.DepartureRunway) bool {
						return r.Airport == ap && r.Runway == rwy.Id
					}) {
						sumRunwayVecs = math.Add2f(sumRunwayVecs, math.HeadingVector(rwy.Heading))
					}
					if slices.ContainsFunc(c.ScenarioSpec.ArrivalRunways, func(r sim.ArrivalRunway) bool {
						return r.Airport == ap && r.Runway == rwy.Id
					}) {
						sumRunwayVecs = math.Add2f(sumRunwayVecs, math.HeadingVector(rwy.Heading))
					}
				}
			}
			avgRwyHeading := math.VectorHeading(sumRunwayVecs)
			avgRwyMagneticHeading := avgRwyHeading + c.ScenarioSpec.MagneticVariation

			sampledMETAR = wx.SampleMETAR(apMETAR, c.availableWXIntervals, avgRwyMagneticHeading)
		}

		if sampledMETAR != nil {
			c.StartTime = sampledMETAR.Time.UTC()

			// Set VFR launch rate to zero if selected weather is IMC
			if !sampledMETAR.IsVMC() {
				c.ScenarioSpec.LaunchConfig.VFRDepartureRateScale = 0
			}
		}
	}
}

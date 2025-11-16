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
	server.NewSimConfiguration

	selectedTRACONConfigs map[string]*server.Configuration

	displayError error

	mgr            *client.ConnectionManager
	selectedServer *client.Server
	defaultTRACON  *string
	tfrCache       *av.TFRCache
	lg             *log.Logger

	// UI state
	newSimType       newSimType
	connectionConfig server.SimConnectionConfiguration

	mu              util.LoggingMutex // protects airportMETAR/fetchingMETAR/availableWXIntervals
	airportMETAR    map[string][]wx.METAR
	metarAirports   []string
	fetchMETARError error
	fetchingMETAR   bool
	metarGeneration int

	availableWXIntervals []util.TimeInterval
}

func MakeNewSimConfiguration(mgr *client.ConnectionManager, defaultTRACON *string, tfrCache *av.TFRCache, lg *log.Logger) *NewSimConfiguration {
	c := &NewSimConfiguration{
		lg:                  lg,
		mgr:                 mgr,
		selectedServer:      mgr.LocalServer,
		defaultTRACON:       defaultTRACON,
		tfrCache:            tfrCache,
		NewSimConfiguration: server.MakeNewSimConfiguration(),
	}

	c.SetTRACON(*defaultTRACON)

	return c
}

func (c *NewSimConfiguration) SetTRACON(name string) {
	var ok bool
	configs := c.selectedServer.GetConfigs()
	if c.selectedTRACONConfigs, ok = configs[name]; !ok {
		if name != "" {
			c.lg.Errorf("%s: TRACON not found!", name)
		}
		// Pick one at random
		name = util.SortedMapKeys(configs)[rand.Make().Intn(len(configs))]
		c.selectedTRACONConfigs = configs[name]
	}
	c.TRACONName = name
	var groupConfig *server.Configuration
	c.GroupName, groupConfig = util.FirstSortedMapEntry(c.selectedTRACONConfigs)

	c.SetScenario(c.GroupName, groupConfig.DefaultScenario)
}

func (c *NewSimConfiguration) SetScenario(groupName, scenarioName string) {
	var ok bool
	var groupConfig *server.Configuration
	if groupConfig, ok = c.selectedTRACONConfigs[groupName]; !ok {
		c.lg.Errorf("%s: group not found in TRACON %s", groupName, c.TRACONName)
		groupName, groupConfig = util.FirstSortedMapEntry(c.selectedTRACONConfigs)
	}
	c.GroupName = groupName

	if c.ScenarioConfig, ok = groupConfig.ScenarioConfigs[scenarioName]; !ok {
		if scenarioName != "" {
			c.lg.Errorf("%s: scenario not found in group %s", scenarioName, c.GroupName)
		}
		scenarioName = groupConfig.DefaultScenario
		c.ScenarioConfig = groupConfig.ScenarioConfigs[scenarioName]
	}
	c.ScenarioName = scenarioName

	c.fetchMETAR()

	c.updateStartTimeForRunways()
}

func (c *NewSimConfiguration) fetchMETAR() {
	c.mu.Lock(c.lg)
	defer c.mu.Unlock(c.lg)

	if c.ScenarioConfig == nil || c.selectedServer == nil {
		c.fetchingMETAR = false
		return
	}

	airports := c.ScenarioConfig.AllAirports()
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
			c.updateStartTimeForRunways()
			c.computeAvailableWXIntervals()
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
		if intervals, ok := c.selectedServer.AvailableWXByTRACON[c.TRACONName]; ok {
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
	NewSimCreateRemoteSingle
	NewSimCreateRemoteMulti
	NewSimJoinRemote
)

type newSimType int32

func (n newSimType) String() string {
	return []string{
		"Create local single-controller sim",
		"Create single-controller sim on public vice server",
		"Create multi-controller sim on public vice server",
		"Join multi-controller sim on public vice server"}[n]
}

func (c *NewSimConfiguration) UIButtonText() string {
	return util.Select(c.newSimType == NewSimJoinRemote, "Join", "Next")
}

func (c *NewSimConfiguration) ShowRatesWindow() bool {
	return c.newSimType != NewSimJoinRemote
}

// facilityHasMatchingScenarios checks if a facility has any scenarios matching the current sim type filter
func (c *NewSimConfiguration) facilityHasMatchingScenarios(facility string, configsByFacility map[string]map[string]*server.Configuration) bool {
	groups := configsByFacility[facility]
	for _, group := range groups {
		if c.groupHasMatchingScenarios(group) {
			return true
		}
	}
	return false
}

// groupHasMatchingScenarios checks if a group has any scenarios matching the current sim type filter
func (c *NewSimConfiguration) groupHasMatchingScenarios(group *server.Configuration) bool {
	if group == nil || group.ScenarioConfigs == nil {
		return false
	}
	for _, scenarioConfig := range group.ScenarioConfigs {
		if c.scenarioMatchesFilter(scenarioConfig) {
			return true
		}
	}
	return false
}

// scenarioMatchesFilter checks if a scenario matches the current sim type filter
func (c *NewSimConfiguration) scenarioMatchesFilter(scenarioConfig *server.SimScenarioConfiguration) bool {
	if c.newSimType == NewSimCreateLocal || c.newSimType == NewSimCreateRemoteSingle {
		// Single-controller mode; all scenarios are valid.
		return true
	} else if c.newSimType == NewSimCreateRemoteMulti {
		// Multi-controller mode
		numControllers := 0
		if defaultSplit, ok := scenarioConfig.SplitConfigurations["default"]; ok {
			numControllers = len(defaultSplit)
		}
		return numControllers > 1
	}
	return true
}

func (c *NewSimConfiguration) DrawUI(p platform.Platform, config *Config) bool {
	if err := c.mgr.UpdateRemoteSims(); err != nil {
		c.lg.Warnf("UpdateRemoteSims: %v", err)
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
	var runningSims map[string]*server.RemoteSim
	if c.mgr.RemoteServer != nil {
		// Filter out full sims
		runningSims = maps.Collect(util.FilterSeq2(maps.All(c.mgr.RemoteServer.GetRunningSims()),
			func(name string, rs *server.RemoteSim) bool {
				return len(rs.AvailablePositions) > 0
			}))

		if imgui.BeginTableV("server", 2, 0, imgui.Vec2{tableScale * 500, 0}, 0.) {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Sim options:")

			origType := c.newSimType

			doButton := func(ty newSimType, srv *client.Server) {
				if imgui.RadioButtonIntPtr(ty.String(), (*int32)(&c.newSimType), int32(ty)) && origType != ty {
					c.selectedServer = srv
					c.SetTRACON(c.TRACONName)
					c.displayError = nil
				}
			}

			imgui.TableNextColumn()
			doButton(NewSimCreateLocal, c.mgr.LocalServer)

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TableNextColumn()
			doButton(NewSimCreateRemoteSingle, c.mgr.RemoteServer)

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TableNextColumn()
			doButton(NewSimCreateRemoteMulti, c.mgr.RemoteServer)

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TableNextColumn()

			if len(runningSims) == 0 {
				imgui.BeginDisabled()
				if c.newSimType == NewSimJoinRemote {
					c.newSimType = NewSimCreateRemoteMulti
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
		imgui.Text("Unable to connect to the multi-controller vice server; " +
			"only single-player scenarios are available.")
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
		config       *server.SimScenarioConfiguration
	}

	getARTCCForFacility := func(facility string, info *server.Configuration) string {
		if info != nil && info.ARTCC != "" {
			return info.ARTCC
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

	formatFacilityLabel := func(facility string, info *server.Configuration) string {
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

	if c.newSimType == NewSimCreateLocal || c.newSimType == NewSimCreateRemoteMulti || c.newSimType == NewSimCreateRemoteSingle {
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
			configsByFacility := c.selectedServer.GetConfigs()
			allFacilities := util.SortedMapKeys(configsByFacility)
			facilityInfo := make(map[string]*server.Configuration, len(configsByFacility))
			for facility, groups := range configsByFacility {
				for _, cfg := range groups {
					facilityInfo[facility] = cfg
					break
				}
			}

			selectedARTCC := ""
			if c.TRACONName != "" {
				selectedARTCC = getARTCCForFacility(c.TRACONName, facilityInfo[c.TRACONName])
			}

			// Collect unique ARTCCs with matching scenarios
			artccs := make(map[string]interface{})
			for facility, info := range facilityInfo {
				if info == nil || !c.facilityHasMatchingScenarios(facility, configsByFacility) {
					continue
				}
				artcc := getARTCCForFacility(facility, info)
				artccs[artcc] = nil
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
							return getARTCCForFacility(facility, facilityInfo[facility]) == artcc
						})
						if idx >= 0 {
							c.SetTRACON(allFacilities[idx])
						}
					}
				}
			}
			imgui.EndChild()

			// Column 2: TRACONs or ARTCC areas for selected ARTCC
			imgui.TableNextColumn()
			if imgui.BeginChildStrV("tracons/areas", imgui.Vec2{tableScale * 150, tableScale * 350}, 0, imgui.WindowFlagsNoResize) {
				for _, facility := range allFacilities {
					info := facilityInfo[facility]
					if info == nil {
						continue
					}
					artcc := getARTCCForFacility(facility, info)
					if selectedARTCC != "" && artcc != selectedARTCC {
						continue
					}

					// Build area/group structure for this facility
					groups := configsByFacility[facility]
					_, isTRACON := av.DB.TRACONs[facility]
					areaToGroups := make(map[string]*areaInfo)

					for groupName, gcfg := range groups {
						if !c.groupHasMatchingScenarios(gcfg) {
							continue
						}
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
					if imgui.SelectableBoolV(label, facility == c.TRACONName, 0, imgui.Vec2{}) && facility != c.TRACONName {
						c.SetTRACON(facility)
					}

					// Display sub-items (groups for TRACONs, areas for ARTCCs)
					if facility == c.TRACONName {
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
									c.SetScenario(firstGroup, groups[firstGroup].DefaultScenario)
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
				selectedGroup := c.selectedTRACONConfigs[c.GroupName]
				if selectedGroup != nil {
					// Use same area logic as column 2: for TRACONs, use group name; for ARTCCs, use Area field
					_, isTRACON := av.DB.TRACONs[c.TRACONName]
					selectedArea := c.GroupName
					if !isTRACON {
						selectedArea = trimFacilityName(selectedGroup.Area, "Area")
					}

					// Collect all scenarios from groups with the same area
					var allScenarios []scenarioInfo
					for groupName, group := range c.selectedTRACONConfigs {
						groupArea := groupName
						if !isTRACON {
							groupArea = trimFacilityName(group.Area, "Area")
						}
						if groupArea == selectedArea {
							for name, scenarioConfig := range group.ScenarioConfigs {
								if c.scenarioMatchesFilter(scenarioConfig) {
									allScenarios = append(allScenarios, scenarioInfo{
										groupName:    groupName,
										scenarioName: name,
										config:       scenarioConfig,
									})
								}
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

		if sc := c.ScenarioConfig.SplitConfigurations; sc.Len() > 1 {
			if imgui.BeginComboV("Split", c.ScenarioConfig.SelectedSplit, imgui.ComboFlagsHeightLarge) {
				for _, split := range sc.Splits() {
					if imgui.SelectableBoolV(split, split == c.ScenarioConfig.SelectedSplit, 0, imgui.Vec2{}) {
						var err error
						c.ScenarioConfig.SelectedSplit = split
						c.ScenarioConfig.SelectedController, err = sc.GetPrimaryController(split)
						if err != nil {
							c.lg.Errorf("unable to find primary controller: %v", err)
						}
					}
				}
				imgui.EndCombo()
			}
		}

		if c.newSimType == NewSimCreateRemoteMulti || c.newSimType == NewSimCreateRemoteSingle {
			imgui.Text("Name: " + c.NewSimName)
		}

		fmtPosition := func(id string) string {
			if tracon := c.selectedTRACONConfigs[c.GroupName]; tracon != nil {
				if ctrl, ok := tracon.ControlPositions[id]; ok {
					id += " (" + ctrl.Position + ")"
				}
			}
			return id
		}

		if len(c.ScenarioConfig.ArrivalRunways) > 0 {
			var a []string
			for _, rwy := range c.ScenarioConfig.ArrivalRunways {
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

		imgui.Text("Control Position: " + fmtPosition(c.ScenarioConfig.SelectedController))

		if c.newSimType == NewSimCreateRemoteMulti || c.newSimType == NewSimCreateRemoteSingle {
			// Various extras only for remote sims
			imgui.Checkbox("Allow Instructor/RPO Sign-ins", &c.AllowInstructorRPO)

			if c.AllowInstructorRPO {
				imgui.Text("Sign in as:")
				var curPos int32 // 0 -> primaryController
				if c.connectionConfig.Position == "INS" {
					curPos = 1
				} else if c.connectionConfig.Position == "RPO" {
					curPos = 2
				}
				if imgui.RadioButtonIntPtr(c.ScenarioConfig.SelectedController, &curPos, 0) {
					c.connectionConfig.Position = "" // default: server will sort it out
				}
				if imgui.RadioButtonIntPtr("Instructor", &curPos, 1) {
					c.connectionConfig.Position = "INS"
				}
				if imgui.RadioButtonIntPtr("RPO", &curPos, 2) {
					c.connectionConfig.Position = "RPO"
				}

				// Allow instructor mode for regular controllers when not signing in as dedicated instructor/RPO
				if c.connectionConfig.Position == "" {
					imgui.Checkbox("Also sign in as Instructor", &c.Instructor)
				}
			}

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
			c.connectionConfig.DisableTextToSpeech = config.DisableTextToSpeech
			c.NewSimConfiguration.DisableTextToSpeech = config.DisableTextToSpeech
		}

		imgui.Checkbox("Ensure last two characters in callsigns are unique",
			&c.NewSimConfiguration.EnforceUniqueCallsignSuffix)

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
			if idx := slices.Index(metarAirports, c.ScenarioConfig.PrimaryAirport); idx > 0 {
				// Move primary to the front
				metarAirports = slices.Delete(metarAirports, idx, idx+1)
				metarAirports = slices.Insert(metarAirports, 0, c.ScenarioConfig.PrimaryAirport)
			}

			metar := c.airportMETAR[metarAirports[0]]
			TimePicker("Simulation start date/time", &c.NewSimConfiguration.StartTime,
				c.availableWXIntervals, metar, &ui.fixedFont.Ifont)

			imgui.SameLine()
			if imgui.Button(renderer.FontAwesomeIconRedo) {
				c.updateStartTimeForRunways()
			}

			tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
			if imgui.BeginTableV("metar", 2, imgui.TableFlagsSizingFixedFit, imgui.Vec2{tableScale * 800, 0}, 0.) {
				for i, ap := range metarAirports {
					imgui.TableNextRow()
					imgui.TableNextColumn()
					if i == 0 {
						imgui.Text("METAR:")
					}
					imgui.TableNextColumn()
					imgui.PushFont(&ui.fixedFont.Ifont)
					metar := wx.METARForTime(c.airportMETAR[ap], c.NewSimConfiguration.StartTime)
					imgui.Text(metar.Raw)
					imgui.PopFont()
				}
				imgui.EndTable()
			}
		}
	} else {
		// Join remote
		rs, ok := runningSims[c.connectionConfig.RemoteSim]
		if !ok || c.connectionConfig.RemoteSim == "" {
			c.connectionConfig.RemoteSim, rs = util.FirstSortedMapEntry(runningSims)

			if _, ok := rs.CoveredPositions[rs.PrimaryController]; !ok {
				// If the primary position isn't currently covered, make that the default selection.
				c.connectionConfig.Position = rs.PrimaryController
			}
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
				if len(rs.AvailablePositions) == 0 {
					// No open positions left; don't even offer it.
					continue
				}

				imgui.PushIDStr(simName)
				imgui.TableNextRow()
				imgui.TableNextColumn()

				// Indicate if a password is required
				if rs.RequirePassword {
					imgui.Text(renderer.FontAwesomeIconLock)
				}
				imgui.TableNextColumn()

				selected := simName == c.connectionConfig.RemoteSim
				selFlags := imgui.SelectableFlagsSpanAllColumns | imgui.SelectableFlagsNoAutoClosePopups
				if imgui.SelectableBoolV(simName, selected, selFlags, imgui.Vec2{}) {
					c.connectionConfig.RemoteSim = simName

					rs = runningSims[c.connectionConfig.RemoteSim]
					if _, ok := rs.CoveredPositions[rs.PrimaryController]; !ok {
						// If the primary position isn't currently covered, make that the default selection.
						c.connectionConfig.Position = rs.PrimaryController
					}
				}

				imgui.TableNextColumn()
				imgui.Text(runningSims[simName].ScenarioName)

				imgui.TableNextColumn()
				covered, available := len(rs.CoveredPositions), len(rs.AvailablePositions)
				controllers := fmt.Sprintf("%d / %d", covered, covered+available)
				imgui.Text(controllers)
				if imgui.IsItemHovered() && len(rs.CoveredPositions) > 0 {
					imgui.SetTooltip(strings.Join(util.SortedMapKeys(rs.CoveredPositions), ", "))
				}

				imgui.PopID()
			}
			imgui.EndTable()
		}

		// Handle the case of someone else signing in to the position
		if _, ok := rs.AvailablePositions[c.connectionConfig.Position]; !ok {
			c.connectionConfig.Position, _ = util.FirstSortedMapEntry(rs.AvailablePositions)
		}

		fmtPosition := func(id string) string {
			if ctrl, ok := rs.AvailablePositions[id]; ok {
				id += " (" + ctrl.Position + ")"
			}
			return id
		}

		if imgui.BeginCombo("Position", fmtPosition(c.connectionConfig.Position)) {
			for pos := range util.SortedMap(rs.AvailablePositions) {
				if pos[0] == '_' {
					continue
				}

				if imgui.SelectableBoolV(fmtPosition(pos), c.connectionConfig.Position == pos, 0, imgui.Vec2{}) {
					c.connectionConfig.Position = pos
				}
			}

			imgui.EndCombo()
		}
		if rs.RequirePassword {
			imgui.InputTextMultiline("Password", &c.connectionConfig.Password, imgui.Vec2{}, 0, nil)
		}
	}

	return false
}

func (c *NewSimConfiguration) DrawRatesUI(p platform.Platform) bool {
	// Check rate limits and clamp if necessary
	const rateLimit = 100.0
	rateClamped := false
	if !c.ScenarioConfig.LaunchConfig.CheckRateLimits(rateLimit) {
		c.ScenarioConfig.LaunchConfig.ClampRates(rateLimit)
		rateClamped = true
	}

	// Display error message if rates were clamped
	if rateClamped {
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .5, .5, 1})
		imgui.Text(fmt.Sprintf("Launch rates will be reduced to stay within the %d aircraft/hour limit", int(rateLimit)))
		imgui.PopStyleColor()
	}

	drawDepartureUI(&c.ScenarioConfig.LaunchConfig, p)
	drawVFRDepartureUI(&c.ScenarioConfig.LaunchConfig, p)
	drawArrivalUI(&c.ScenarioConfig.LaunchConfig, p)
	drawOverflightUI(&c.ScenarioConfig.LaunchConfig, p)
	return false
}

func (c *NewSimConfiguration) OkDisabled() bool {
	return (c.newSimType == NewSimCreateRemoteMulti || c.newSimType == NewSimCreateRemoteSingle) && (c.NewSimName == "" || (c.RequirePassword && c.Password == ""))
}

func (c *NewSimConfiguration) Start() error {
	c.TFRs = c.tfrCache.TFRsForTRACON(c.TRACONName, c.lg)

	if c.newSimType == NewSimJoinRemote {
		// Set the instructor flag from the main config
		c.connectionConfig.Instructor = c.Instructor
		if err := c.mgr.ConnectToSim(c.connectionConfig, c.selectedServer, c.lg); err != nil {
			c.lg.Errorf("ConnectToSim failed: %v", err)
			return err
		}
	} else {
		// Create sim configuration for new sim
		if err := c.mgr.CreateNewSim(c.NewSimConfiguration, c.selectedServer, c.lg); err != nil {
			c.lg.Errorf("CreateNewSim failed: %v", err)
			return err
		}
	}

	*c.defaultTRACON = c.TRACONName
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

///////////////////////////////////////////////////////////////////////////

type LaunchControlWindow struct {
	client              *client.ControlClient
	departures          []*LaunchDeparture
	vfrDepartures       []*LaunchDeparture
	arrivalsOverflights []*LaunchArrivalOverflight
	lg                  *log.Logger
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
	if lc.client.State.MultiControllers != nil {
		imgui.Text("Controlling controller: " + util.Select(ctrl == "", "(none)", ctrl))
		if ctrl == lc.client.State.UserTCP {
			if imgui.Button("Release launch control") {
				lc.client.TakeOrReturnLaunchControl(eventStream)
			}
		} else {
			if imgui.Button("Take launch control") {
				lc.client.TakeOrReturnLaunchControl(eventStream)
			}
		}
	}

	canLaunch := ctrl == lc.client.State.UserTCP || (lc.client.State.MultiControllers == nil && ctrl == "") ||
		lc.client.State.AreInstructorOrRPO(lc.client.State.UserTCP)
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
		if imgui.BeginTableV("controllers", 4, tableFlags, imgui.Vec2{}, 0) {
			imgui.TableSetupColumn("CID")
			imgui.TableSetupColumn("Human")
			imgui.TableSetupColumn("Frequency")
			imgui.TableSetupColumn("Name")
			imgui.TableHeadersRow()

			// Sort 2-char before 3-char and then alphabetically
			sorted := slices.Collect(maps.Keys(c.State.Controllers))
			slices.SortFunc(sorted, func(a, b string) int {
				if len(a) < len(b) {
					return -1
				} else if len(a) > len(b) {
					return 1
				} else {
					return strings.Compare(a, b)
				}
			})

			for _, id := range sorted {
				ctrl := c.State.Controllers[id]
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(ctrl.Id())
				imgui.TableNextColumn()
				if slices.Contains(c.State.HumanControllers, ctrl.Id()) {
					sq := renderer.FontAwesomeIconCheckSquare
					// Center the square in the column
					// https://stackoverflow.com/a/66109051
					pos := imgui.CursorPosX() + float32(imgui.ColumnWidth()) - imgui.CalcTextSize(sq).X - imgui.ScrollX() -
						2*imgui.CurrentStyle().ItemSpacing().X
					if pos > imgui.CursorPosX() {
						imgui.SetCursorPos(imgui.Vec2{X: pos, Y: imgui.CursorPos().Y})
					}
					imgui.Text(sq)
				}
				imgui.TableNextColumn()
				imgui.Text(ctrl.Frequency.String())
				imgui.TableNextColumn()
				imgui.Text(ctrl.Position)
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
	if c.ScenarioConfig == nil || c.selectedServer == nil || c.airportMETAR == nil {
		return
	}

	airports := c.ScenarioConfig.AllAirports()
	if len(airports) == 0 {
		return
	}
	var ap string
	if slices.Contains(airports, c.ScenarioConfig.PrimaryAirport) {
		ap = c.ScenarioConfig.PrimaryAirport
	} else {
		ap = airports[0]
	}

	// Use METAR sampling with wind specifier if available
	if apMETAR, ok := c.airportMETAR[ap]; ok && len(apMETAR) > 0 {
		var sampledMETAR *wx.METAR

		if c.ScenarioConfig.WindSpecifier != nil {
			// Use the scenario's wind specifier
			sampledMETAR = wx.SampleMETARWithSpec(apMETAR, c.availableWXIntervals,
				c.ScenarioConfig.WindSpecifier, c.ScenarioConfig.MagneticVariation)
		} else {
			// Fallback: calculate average runway heading and use that
			var sumRunwayVecs [2]float32
			if dbap, ok := av.DB.Airports[ap]; ok {
				for _, rwy := range dbap.Runways {
					if slices.ContainsFunc(c.ScenarioConfig.DepartureRunways, func(r sim.DepartureRunway) bool {
						return r.Airport == ap && r.Runway == rwy.Id
					}) {
						sumRunwayVecs = math.Add2f(sumRunwayVecs, math.HeadingVector(rwy.Heading))
					}
					if slices.ContainsFunc(c.ScenarioConfig.ArrivalRunways, func(r sim.ArrivalRunway) bool {
						return r.Airport == ap && r.Runway == rwy.Id
					}) {
						sumRunwayVecs = math.Add2f(sumRunwayVecs, math.HeadingVector(rwy.Heading))
					}
				}
			}
			avgRwyHeading := math.VectorHeading(sumRunwayVecs)
			avgRwyMagneticHeading := avgRwyHeading + c.ScenarioConfig.MagneticVariation

			sampledMETAR = wx.SampleMETAR(apMETAR, c.availableWXIntervals, avgRwyMagneticHeading)
		}

		if sampledMETAR != nil {
			c.StartTime = sampledMETAR.Time.UTC()

			// Set VFR launch rate to zero if selected weather is IMC
			if !sampledMETAR.IsVMC() {
				c.ScenarioConfig.LaunchConfig.VFRDepartureRateScale = 0
			}
		}
	}
}

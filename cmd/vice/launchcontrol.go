// launchcontrol.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

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

	// Spawn aircraft for any types in manual mode
	if config.DepartureMode == sim.LaunchManual {
		lc.spawnDepartures()
	}
	if config.ArrivalMode == sim.LaunchManual {
		lc.spawnArrivals()
	}
	if config.OverflightMode == sim.LaunchManual {
		lc.spawnOverflights()
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

func (lc *LaunchControlWindow) spawnDepartures() {
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
}

func (lc *LaunchControlWindow) spawnArrivals() {
	for i := range lc.arrivalsOverflights {
		if lc.arrivalsOverflights[i].Airport != "overflights" &&
			lc.arrivalsOverflights[i].Aircraft.ADSBCallsign == "" {
			lc.spawnArrivalOverflight(lc.arrivalsOverflights[i])
		}
	}
}

func (lc *LaunchControlWindow) spawnOverflights() {
	for i := range lc.arrivalsOverflights {
		if lc.arrivalsOverflights[i].Airport == "overflights" &&
			lc.arrivalsOverflights[i].Aircraft.ADSBCallsign == "" {
			lc.spawnArrivalOverflight(lc.arrivalsOverflights[i])
		}
	}
}

func (lc *LaunchControlWindow) cleanupDepartures() {
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

	if len(toDelete) > 0 {
		lc.client.DeleteAircraft(toDelete, func(err error) {
			if err != nil {
				lc.lg.Errorf("Error deleting aircraft: %v", err)
			}
		})
	}
}

func (lc *LaunchControlWindow) cleanupArrivals() {
	var toDelete []sim.Aircraft

	for i := range lc.arrivalsOverflights {
		if lc.arrivalsOverflights[i].Airport != "overflights" &&
			lc.arrivalsOverflights[i].Aircraft.ADSBCallsign != "" {
			toDelete = append(toDelete, lc.arrivalsOverflights[i].Aircraft)
			lc.arrivalsOverflights[i].Aircraft = sim.Aircraft{}
		}
	}

	if len(toDelete) > 0 {
		lc.client.DeleteAircraft(toDelete, func(err error) {
			if err != nil {
				lc.lg.Errorf("Error deleting aircraft: %v", err)
			}
		})
	}
}

func (lc *LaunchControlWindow) cleanupOverflights() {
	var toDelete []sim.Aircraft

	for i := range lc.arrivalsOverflights {
		if lc.arrivalsOverflights[i].Airport == "overflights" &&
			lc.arrivalsOverflights[i].Aircraft.ADSBCallsign != "" {
			toDelete = append(toDelete, lc.arrivalsOverflights[i].Aircraft)
			lc.arrivalsOverflights[i].Aircraft = sim.Aircraft{}
		}
	}

	if len(toDelete) > 0 {
		lc.client.DeleteAircraft(toDelete, func(err error) {
			if err != nil {
				lc.lg.Errorf("Error deleting aircraft: %v", err)
			}
		})
	}
}

func (lc *LaunchControlWindow) cleanupAllAircraft() {
	lc.cleanupDepartures()
	lc.cleanupArrivals()
	lc.cleanupOverflights()
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
		// Simulation controls row
		if lc.client != nil && lc.client.Connected() {
			if lc.client.State.Paused {
				if imgui.Button(renderer.FontAwesomeIconPlayCircle + " Resume") {
					lc.client.ToggleSimPause()
				}
			} else {
				if imgui.Button(renderer.FontAwesomeIconPauseCircle + " Pause") {
					lc.client.ToggleSimPause()
				}
			}
		}

		imgui.SameLine()
		if imgui.Button(renderer.FontAwesomeIconTrash + " Delete All") {
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

		// Emergency selector row (if emergencies available)
		if etypes := lc.client.State.Emergencies; len(etypes) > 0 {
			emergencyLabel := func(et sim.Emergency) string {
				return et.Name + " (" + et.ApplicableTo.String() + ")"
			}
			imgui.Text("Emergency:")
			imgui.SameLine()
			imgui.SetNextItemWidth(250)
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

		imgui.Separator()

		flags := imgui.TableFlagsBordersH | imgui.TableFlagsBordersOuterV | imgui.TableFlagsRowBg |
			imgui.TableFlagsSizingStretchProp
		tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))

		// Helper function for manual launch UI to show MIT and time since last launch
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

		changed := false

		// Departures section - check DepartureMode
		if imgui.CollapsingHeaderBoolPtr("Departures", nil) {
			imgui.Text("Aircraft spawn:")
			imgui.SameLine()
			if imgui.RadioButtonIntPtr("Manual##dep", &lc.client.State.LaunchConfig.DepartureMode, sim.LaunchManual) {
				lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
				lc.spawnDepartures()
			}
			imgui.SameLine()
			if imgui.RadioButtonIntPtr("Automatic##dep", &lc.client.State.LaunchConfig.DepartureMode, sim.LaunchAutomatic) {
				lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
				lc.cleanupDepartures()
			}

			if lc.client.State.LaunchConfig.DepartureMode == sim.LaunchManual {
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
			} else {
				changed = drawDepartureUI(&lc.client.State.LaunchConfig, p) || changed
			}
		}

		// VFR Departures section - check DepartureMode
		if len(lc.vfrDepartures) > 0 && imgui.CollapsingHeaderBoolPtr("VFR Departures", nil) {
			imgui.Text("Aircraft spawn:")
			imgui.SameLine()
			if imgui.RadioButtonIntPtr("Manual##vfrdep", &lc.client.State.LaunchConfig.DepartureMode, sim.LaunchManual) {
				lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
				lc.spawnDepartures()
			}
			imgui.SameLine()
			if imgui.RadioButtonIntPtr("Automatic##vfrdep", &lc.client.State.LaunchConfig.DepartureMode, sim.LaunchAutomatic) {
				lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
				lc.cleanupDepartures()
			}

			if lc.client.State.LaunchConfig.DepartureMode == sim.LaunchManual {
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
			} else {
				changed = drawVFRDepartureUI(&lc.client.State.LaunchConfig, p) || changed
			}
		}

		// Arrivals section - check ArrivalMode
		if imgui.CollapsingHeaderBoolPtr("Arrivals", nil) {
			imgui.Text("Aircraft spawn:")
			imgui.SameLine()
			if imgui.RadioButtonIntPtr("Manual##arr", &lc.client.State.LaunchConfig.ArrivalMode, sim.LaunchManual) {
				lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
				lc.spawnArrivals()
			}
			imgui.SameLine()
			if imgui.RadioButtonIntPtr("Automatic##arr", &lc.client.State.LaunchConfig.ArrivalMode, sim.LaunchAutomatic) {
				lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
				lc.cleanupArrivals()
			}

			if lc.client.State.LaunchConfig.ArrivalMode == sim.LaunchManual {
				// Filter to only show arrivals (not overflights)
				arrivals := util.FilterSlice(lc.arrivalsOverflights, func(ao *LaunchArrivalOverflight) bool {
					return ao.Airport != "overflights"
				})

				narr := util.ReduceSlice(arrivals, func(arr *LaunchArrivalOverflight, n int) int {
					return n + arr.TotalLaunches
				}, 0)

				imgui.Text(fmt.Sprintf("Arrivals: %d total", narr))

				sortedArrivals := util.DuplicateSlice(arrivals)
				slices.SortFunc(sortedArrivals, func(a, b *LaunchArrivalOverflight) int {
					return strings.Compare(a.Airport+"/"+a.Group, b.Airport+"/"+b.Group)
				})

				maxGroups, numGroups := 0, 1
				lastAirport := ""
				for _, ao := range sortedArrivals {
					if ao.Airport != lastAirport {
						maxGroups = max(maxGroups, numGroups)
						lastAirport = ao.Airport
						numGroups = 1
					} else {
						numGroups++
					}
				}
				numColumns := min(maxGroups, 3)

				if len(sortedArrivals) > 0 && imgui.BeginTableV("arrivals", int32(1+7*numColumns), flags, imgui.Vec2{tableScale * float32(100+350*numColumns), 0}, 0.0) {
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
					for _, arr := range sortedArrivals {
						if arr.Airport != lastAirport {
							imgui.TableNextRow()
							lastAirport = arr.Airport
							curColumn = 0
							imgui.TableNextColumn()
							imgui.Text(arr.Airport)
						} else if curColumn+1 == numColumns {
							curColumn = 0
							imgui.TableNextRow()
							imgui.TableNextColumn()
							imgui.Text("")
						} else {
							curColumn++
						}

						imgui.PushIDStr(arr.Group + arr.Airport)

						imgui.TableNextColumn()
						imgui.Text(arr.Group)

						imgui.TableNextColumn()
						imgui.Text(strconv.Itoa(arr.TotalLaunches))

						if arr.Aircraft.ADSBCallsign != "" {
							imgui.TableNextColumn()
							imgui.Text(arr.Aircraft.FlightPlan.AircraftType)

							mitAndTime(&arr.Aircraft, arr.Aircraft.Position(), arr.LastLaunchCallsign,
								arr.LastLaunchTime)

							imgui.TableNextColumn()
							if imgui.Button(renderer.FontAwesomeIconPlaneDeparture) {
								lc.client.LaunchArrivalOverflight(arr.Aircraft)
								arr.LastLaunchCallsign = arr.Aircraft.ADSBCallsign
								arr.LastLaunchTime = lc.client.CurrentTime()
								arr.TotalLaunches++

								arr.Aircraft = sim.Aircraft{}
								lc.spawnArrivalOverflight(arr)
							}

							imgui.TableNextColumn()
							if imgui.Button(renderer.FontAwesomeIconRedo) {
								arr.Aircraft = sim.Aircraft{}
								lc.spawnArrivalOverflight(arr)
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
			} else {
				changed = drawArrivalUI(&lc.client.State.LaunchConfig, p) || changed
			}
		}

		// Overflights section - check OverflightMode
		if imgui.CollapsingHeaderBoolPtr("Overflights", nil) {
			imgui.Text("Aircraft spawn:")
			imgui.SameLine()
			if imgui.RadioButtonIntPtr("Manual##of", &lc.client.State.LaunchConfig.OverflightMode, sim.LaunchManual) {
				lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
				lc.spawnOverflights()
			}
			imgui.SameLine()
			if imgui.RadioButtonIntPtr("Automatic##of", &lc.client.State.LaunchConfig.OverflightMode, sim.LaunchAutomatic) {
				lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
				lc.cleanupOverflights()
			}

			if lc.client.State.LaunchConfig.OverflightMode == sim.LaunchManual {
				// Filter to only show overflights
				overflights := util.FilterSlice(lc.arrivalsOverflights, func(ao *LaunchArrivalOverflight) bool {
					return ao.Airport == "overflights"
				})

				nof := util.ReduceSlice(overflights, func(of *LaunchArrivalOverflight, n int) int {
					return n + of.TotalLaunches
				}, 0)

				imgui.Text(fmt.Sprintf("Overflights: %d total", nof))

				sortedOverflights := util.DuplicateSlice(overflights)
				slices.SortFunc(sortedOverflights, func(a, b *LaunchArrivalOverflight) int {
					return strings.Compare(a.Group, b.Group)
				})

				if len(sortedOverflights) > 0 && imgui.BeginTableV("overflights", 7, flags, imgui.Vec2{tableScale * 400, 0}, 0.0) {
					imgui.TableSetupColumn("Group")
					imgui.TableSetupColumn("#")
					imgui.TableSetupColumn("A/C Type")
					imgui.TableSetupColumn("MIT")
					imgui.TableSetupColumn("Time")
					imgui.TableSetupColumn("")
					imgui.TableSetupColumn("")
					imgui.TableHeadersRow()

					for _, of := range sortedOverflights {
						imgui.TableNextRow()
						imgui.PushIDStr(of.Group + "overflight")

						imgui.TableNextColumn()
						imgui.Text(of.Group)

						imgui.TableNextColumn()
						imgui.Text(strconv.Itoa(of.TotalLaunches))

						if of.Aircraft.ADSBCallsign != "" {
							imgui.TableNextColumn()
							imgui.Text(of.Aircraft.FlightPlan.AircraftType)

							mitAndTime(&of.Aircraft, of.Aircraft.Position(), of.LastLaunchCallsign,
								of.LastLaunchTime)

							imgui.TableNextColumn()
							if imgui.Button(renderer.FontAwesomeIconPlaneDeparture) {
								lc.client.LaunchArrivalOverflight(of.Aircraft)
								of.LastLaunchCallsign = of.Aircraft.ADSBCallsign
								of.LastLaunchTime = lc.client.CurrentTime()
								of.TotalLaunches++

								of.Aircraft = sim.Aircraft{}
								lc.spawnArrivalOverflight(of)
							}

							imgui.TableNextColumn()
							if imgui.Button(renderer.FontAwesomeIconRedo) {
								of.Aircraft = sim.Aircraft{}
								lc.spawnArrivalOverflight(of)
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
			} else {
				changed = drawOverflightUI(&lc.client.State.LaunchConfig, p) || changed
			}
		}

		if changed {
			lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
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
		lc.cleanupAllAircraft()
		ui.showLaunchControl = false
	}
}

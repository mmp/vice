// launchcontrol.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
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
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// LaunchControlWindow drives manual aircraft launches. The pending flights
// themselves live server-side and arrive in the state updates' launch slots;
// the window only keeps its own launch counters and MIT bookkeeping.
type LaunchControlWindow struct {
	client            *client.ControlClient
	lg                *log.Logger
	selectedEmergency int

	// Per-slot launch counts, and the last launch from each runway (IFR and
	// VFR combined) and each inbound flow for the MIT and Time columns.
	launchCounts        map[string]int
	lastRunwayDeparture map[string]launchRecord
	lastInbound         map[string]launchRecord
}

type launchRecord struct {
	Callsign av.ADSBCallsign
	Time     sim.Time
}

func MakeLaunchControlWindow(client *client.ControlClient, lg *log.Logger) *LaunchControlWindow {
	return &LaunchControlWindow{
		client:              client,
		lg:                  lg,
		launchCounts:        make(map[string]int),
		lastRunwayDeparture: make(map[string]launchRecord),
		lastInbound:         make(map[string]launchRecord),
	}
}

func departureSlotKey(slot sim.DepartureLaunchSlot) string {
	return slot.Airport + "/" + string(slot.Runway) + "/" + slot.Category + "/" + strconv.Itoa(int(slot.Rules))
}

func inboundSlotKey(slot sim.InboundLaunchSlot) string {
	return slot.Group + "/" + slot.Airport
}

func (lc *LaunchControlWindow) logLaunchError(callsign av.ADSBCallsign) func(error) {
	return func(err error) {
		if err != nil {
			lc.lg.Warnf("%s: %v", callsign, err)
		}
	}
}

func (lc *LaunchControlWindow) Draw(p platform.Platform, config *Config) {
	showLaunchControls := true
	imgui.SetNextWindowSizeConstraints(imgui.Vec2{300, 100}, imgui.Vec2{-1, float32(p.WindowSize()[1]) * 19 / 20})
	applyPinWindowClass("Launch Control", config, p)
	imgui.BeginV("Launch Control", &showLaunchControls, imgui.WindowFlagsAlwaysAutoResize)
	drawPinButton("Launch Control", config, p)

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
				clear(lc.launchCounts)
				clear(lc.lastRunwayDeparture)
				clear(lc.lastInbound)
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
	mitAndTime := func(launchPosition math.Point2LL, last launchRecord) {
		imgui.TableNextColumn()
		if prev, ok := lc.client.State.GetTrackByCallsign(last.Callsign); ok {
			dist := math.NMDistance2LL(prev.Location, launchPosition)
			imgui.Text(fmt.Sprintf("%.1f", dist))

			imgui.TableNextColumn()

			delta := lc.client.InterpolatedSimTime().Sub(last.Time).Round(time.Second).Seconds()
			m, s := int(delta)/60, int(delta)%60
			imgui.Text(fmt.Sprintf("%02d:%02d", m, s))
		} else {
			imgui.TableNextColumn()
		}
	}

	launchButton := func(slot sim.DepartureLaunchSlot, slotKey string) {
		imgui.TableNextColumn()
		if imgui.Button(renderer.FontAwesomeIconPlaneDeparture) {
			lc.client.LaunchAircraft(slot.LaunchFlight, lc.logLaunchError(slot.Callsign))
			lc.launchCounts[slotKey]++
			lc.lastRunwayDeparture[slot.Airport+"/"+string(slot.Runway)] =
				launchRecord{Callsign: slot.Callsign, Time: lc.client.InterpolatedSimTime()}
		}
		imgui.TableNextColumn()
		if imgui.Button(renderer.FontAwesomeIconRedo) {
			lc.client.RecycleLaunchAircraft(slot.LaunchFlight, lc.logLaunchError(slot.Callsign))
		}
	}

	departureSlots := lc.client.State.DepartureLaunchSlots
	ifrSlots := util.FilterSlice(departureSlots,
		func(slot sim.DepartureLaunchSlot) bool { return slot.Rules == av.FlightRulesIFR })
	vfrSlots := util.FilterSlice(departureSlots,
		func(slot sim.DepartureLaunchSlot) bool { return slot.Rules == av.FlightRulesVFR })

	changed := false

	// Departures section - check DepartureMode
	if imgui.CollapsingHeaderBoolPtr("Departures", nil) {
		imgui.Text("Aircraft spawn:")
		imgui.SameLine()
		if imgui.RadioButtonIntPtr("Manual##dep", &lc.client.State.LaunchConfig.DepartureMode, sim.LaunchManual) {
			lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
		}
		imgui.SameLine()
		if imgui.RadioButtonIntPtr("Automatic##dep", &lc.client.State.LaunchConfig.DepartureMode, sim.LaunchAutomatic) {
			lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
		}

		if lc.client.State.LaunchConfig.DepartureMode == sim.LaunchManual {
			ndep := 0
			for _, slot := range ifrSlots {
				ndep += lc.launchCounts[departureSlotKey(slot)]
			}
			imgui.Text(fmt.Sprintf("Departures: %d total", ndep))

			// The slots arrive sorted by airport, runway, and category.
			// Find the maximum number of slots for any airport.
			maxCategories, curCategories := 0, 1
			lastAp := ""
			for _, slot := range ifrSlots {
				if slot.Airport != lastAp {
					maxCategories = max(maxCategories, curCategories)
					curCategories = 1
					lastAp = slot.Airport
				} else {
					curCategories++
				}
			}

			nColumns := min(3, max(1, maxCategories))
			if len(ifrSlots) > 0 &&
				imgui.BeginTableV("dep", int32(1+9*nColumns), flags, imgui.Vec2{tableScale * float32(100+450*nColumns), 0}, 0.0) {
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
				for _, slot := range ifrSlots {
					if slot.Airport != lastAp {
						imgui.TableNextRow()
						lastAp = slot.Airport
						curColumn = 0

						imgui.TableNextColumn()
						imgui.Text(slot.Airport)
					} else if curColumn+1 == nColumns {
						curColumn = 0
						imgui.TableNextRow()
						imgui.TableNextColumn()
					} else {
						curColumn++
					}

					imgui.TableNextColumn()
					imgui.Text(slot.Runway.Base())
					imgui.TableNextColumn()
					imgui.Text(slot.Category)

					slotKey := departureSlotKey(slot)
					imgui.PushIDStr(slotKey)

					imgui.TableNextColumn()
					imgui.Text(strconv.Itoa(lc.launchCounts[slotKey]))

					if slot.Callsign != "" {
						imgui.TableNextColumn()
						imgui.Text(slot.AircraftType)

						imgui.TableNextColumn()
						imgui.Text(slot.Exit)

						mitAndTime(slot.Position, lc.lastRunwayDeparture[slot.Airport+"/"+string(slot.Runway)])

						launchButton(slot, slotKey)
					} else {
						for range 6 {
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
	if len(lc.client.State.LaunchConfig.VFRAirportRates) > 0 && imgui.CollapsingHeaderBoolPtr("VFR Departures", nil) {
		imgui.Text("Aircraft spawn:")
		imgui.SameLine()
		if imgui.RadioButtonIntPtr("Manual##vfrdep", &lc.client.State.LaunchConfig.DepartureMode, sim.LaunchManual) {
			lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
		}
		imgui.SameLine()
		if imgui.RadioButtonIntPtr("Automatic##vfrdep", &lc.client.State.LaunchConfig.DepartureMode, sim.LaunchAutomatic) {
			lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
		}

		if lc.client.State.LaunchConfig.DepartureMode == sim.LaunchManual {
			ndep := 0
			for _, slot := range vfrSlots {
				ndep += lc.launchCounts[departureSlotKey(slot)]
			}
			imgui.Text(fmt.Sprintf("VFR Departures: %d total", ndep))

			if !lc.client.State.LaunchConfig.HaveVFRReportingRegions {
				imgui.BeginDisabled()
			}
			if imgui.Button("Request Flight Following") {
				lc.client.RequestFlightFollowing()
			}
			if imgui.IsItemHovered() {
				if lc.client.State.LaunchConfig.HaveVFRReportingRegions {
					imgui.SetTooltip("Request VFR flight following from a random VFR aircraft")
				} else {
					imgui.SetTooltip("No flight following airspace configured for this scenario")
				}
			}
			if !lc.client.State.LaunchConfig.HaveVFRReportingRegions {
				imgui.EndDisabled()
			}

			nColumns := min(2, max(1, len(vfrSlots)))
			if len(vfrSlots) > 0 &&
				imgui.BeginTableV("vfrdep", int32(9*nColumns), flags, imgui.Vec2{tableScale * float32(100+450*nColumns), 0}, 0.0) {
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

				for i, slot := range vfrSlots {
					if i%nColumns == 0 {
						imgui.TableNextRow()
					}

					slotKey := departureSlotKey(slot)
					imgui.PushIDStr(slotKey)
					imgui.TableNextColumn()
					imgui.Text(slot.Airport)
					imgui.TableNextColumn()
					imgui.Text(string(slot.Runway))
					imgui.TableNextColumn()
					imgui.Text(strconv.Itoa(lc.launchCounts[slotKey]))

					if slot.Callsign != "" {
						imgui.TableNextColumn()
						imgui.Text(slot.Destination)

						imgui.TableNextColumn()
						imgui.Text(slot.AircraftType)

						mitAndTime(slot.Position, lc.lastRunwayDeparture[slot.Airport+"/"+string(slot.Runway)])

						launchButton(slot, slotKey)
					} else {
						for range 6 {
							imgui.TableNextColumn()
						}
					}

					imgui.PopID()
				}

				imgui.EndTable()
			}
		} else {
			changed = drawVFRDepartureUI(&lc.client.State.LaunchConfig, p) || changed
		}
	}

	inboundLaunch := func(slot sim.InboundLaunchSlot) {
		imgui.TableNextColumn()
		if imgui.Button(renderer.FontAwesomeIconPlaneDeparture) {
			lc.client.LaunchAircraft(slot.LaunchFlight, lc.logLaunchError(slot.Callsign))
			lc.launchCounts[inboundSlotKey(slot)]++
			lc.lastInbound[inboundSlotKey(slot)] =
				launchRecord{Callsign: slot.Callsign, Time: lc.client.InterpolatedSimTime()}
		}
		imgui.TableNextColumn()
		if imgui.Button(renderer.FontAwesomeIconRedo) {
			lc.client.RecycleLaunchAircraft(slot.LaunchFlight, lc.logLaunchError(slot.Callsign))
		}
	}

	// Arrivals section - check ArrivalMode
	if imgui.CollapsingHeaderBoolPtr("Arrivals", nil) {
		imgui.Text("Aircraft spawn:")
		imgui.SameLine()
		if imgui.RadioButtonIntPtr("Manual##arr", &lc.client.State.LaunchConfig.ArrivalMode, sim.LaunchManual) {
			lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
		}
		imgui.SameLine()
		if imgui.RadioButtonIntPtr("Automatic##arr", &lc.client.State.LaunchConfig.ArrivalMode, sim.LaunchAutomatic) {
			lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
		}

		if lc.client.State.LaunchConfig.ArrivalMode == sim.LaunchManual {
			arrivals := util.FilterSlice(lc.client.State.InboundLaunchSlots,
				func(slot sim.InboundLaunchSlot) bool { return slot.Airport != "overflights" })

			narr := 0
			for _, slot := range arrivals {
				narr += lc.launchCounts[inboundSlotKey(slot)]
			}
			imgui.Text(fmt.Sprintf("Arrivals: %d total", narr))

			slices.SortFunc(arrivals, func(a, b sim.InboundLaunchSlot) int {
				return strings.Compare(a.Airport+"/"+a.Group, b.Airport+"/"+b.Group)
			})

			maxGroups, numGroups := 0, 1
			lastAirport := ""
			for _, slot := range arrivals {
				if slot.Airport != lastAirport {
					maxGroups = max(maxGroups, numGroups)
					lastAirport = slot.Airport
					numGroups = 1
				} else {
					numGroups++
				}
			}
			numColumns := min(max(1, maxGroups), 3)

			if len(arrivals) > 0 && imgui.BeginTableV("arrivals", int32(1+7*numColumns), flags, imgui.Vec2{tableScale * float32(100+350*numColumns), 0}, 0.0) {
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
				for _, slot := range arrivals {
					if slot.Airport != lastAirport {
						imgui.TableNextRow()
						lastAirport = slot.Airport
						curColumn = 0
						imgui.TableNextColumn()
						imgui.Text(slot.Airport)
					} else if curColumn+1 == numColumns {
						curColumn = 0
						imgui.TableNextRow()
						imgui.TableNextColumn()
						imgui.Text("")
					} else {
						curColumn++
					}

					imgui.PushIDStr(slot.Group + slot.Airport)

					imgui.TableNextColumn()
					imgui.Text(slot.Group)

					imgui.TableNextColumn()
					imgui.Text(strconv.Itoa(lc.launchCounts[inboundSlotKey(slot)]))

					if slot.Callsign != "" {
						imgui.TableNextColumn()
						imgui.Text(slot.AircraftType)

						mitAndTime(slot.Position, lc.lastInbound[inboundSlotKey(slot)])

						inboundLaunch(slot)
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
		}
		imgui.SameLine()
		if imgui.RadioButtonIntPtr("Automatic##of", &lc.client.State.LaunchConfig.OverflightMode, sim.LaunchAutomatic) {
			lc.client.SetLaunchConfig(lc.client.State.LaunchConfig)
		}

		if lc.client.State.LaunchConfig.OverflightMode == sim.LaunchManual {
			overflights := util.FilterSlice(lc.client.State.InboundLaunchSlots,
				func(slot sim.InboundLaunchSlot) bool { return slot.Airport == "overflights" })

			nof := 0
			for _, slot := range overflights {
				nof += lc.launchCounts[inboundSlotKey(slot)]
			}
			imgui.Text(fmt.Sprintf("Overflights: %d total", nof))

			if len(overflights) > 0 && imgui.BeginTableV("overflights", 7, flags, imgui.Vec2{tableScale * 400, 0}, 0.0) {
				imgui.TableSetupColumn("Group")
				imgui.TableSetupColumn("#")
				imgui.TableSetupColumn("A/C Type")
				imgui.TableSetupColumn("MIT")
				imgui.TableSetupColumn("Time")
				imgui.TableSetupColumn("")
				imgui.TableSetupColumn("")
				imgui.TableHeadersRow()

				for _, slot := range overflights {
					imgui.TableNextRow()
					imgui.PushIDStr(slot.Group + "overflight")

					imgui.TableNextColumn()
					imgui.Text(slot.Group)

					imgui.TableNextColumn()
					imgui.Text(strconv.Itoa(lc.launchCounts[inboundSlotKey(slot)]))

					if slot.Callsign != "" {
						imgui.TableNextColumn()
						imgui.Text(slot.AircraftType)

						mitAndTime(slot.Position, lc.lastInbound[inboundSlotKey(slot)])

						inboundLaunch(slot)
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
		ui.showLaunchControl = false
	}
}

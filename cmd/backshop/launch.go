// cmd/backshop/launch.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/sim"

	"github.com/AllenDang/cimgui-go/imgui"
)

// drawLaunchTab flies one aircraft at a time down a chosen procedure and
// watches it. Launching runs the sim through the whole of the flight at once
// and records it, so what is watched afterwards is a recording: it plays at
// real time to start with, and the scrubber goes both ways, which the sim's own
// clock never does.
func (in *inspector) drawLaunchTab(a *app) {
	if !imgui.BeginTabItem("Launch") {
		return
	}
	defer imgui.EndTabItem()

	if a.cc == nil {
		imgui.Text("No sim running.")
		return
	}

	p := &in.playback
	imgui.TextWrapped("Launching an aircraft flies it from the runway or the gate to wherever " +
		"the sim lets it go, all at once, and plays the flight back below.")

	imgui.SetNextItemWidth(120)
	imgui.InputTextWithHint("Aircraft type", "as scheduled", &in.launchType, 0, nil)
	imgui.SameLine()
	imgui.TextDisabled("(?)")
	tooltip("ICAO type to launch instead of the one the slot sampled,\n" +
		"e.g. B738. Leave empty to fly whatever the scenario picked.")
	if ty := strings.ToUpper(strings.TrimSpace(in.launchType)); ty != "" {
		if _, ok := av.DB.AircraftPerformance[ty]; !ok {
			imgui.TextColored(warningColor, ty+" is not in the performance database")
		}
	}

	imgui.Separator()
	p.drawUI(a)
	if !p.empty() {
		p.drawFlights(a)
	}
	imgui.Separator()

	in.drawLaunchSlots(a)
}

// drawLaunchSlots is the traffic waiting to be flown: the slots the sim keeps
// filled, one per departure runway and inbound flow.
func (in *inspector) drawLaunchSlots(a *app) {
	p := &in.playback
	ss := &a.cc.State

	launch := func(f sim.LaunchFlight) {
		f.AircraftTypeOverride = strings.ToUpper(strings.TrimSpace(in.launchType))
		// A departure from a hold-for-release airport would otherwise sit on
		// the ground for minutes waiting for a release that only a STARS
		// coordination list can give, and backshop has no STARS.
		f.ImmediateTakeoff = true
		p.launch(a, f)
	}
	buttons := func(f sim.LaunchFlight) {
		imgui.BeginDisabledV(p.recording)
		if imgui.SmallButton("Launch") {
			launch(f)
		}
		imgui.SameLine()
		if imgui.SmallButton("Next") {
			a.cc.RecycleLaunchAircraft(f, func(err error) {
				if err != nil {
					a.status = "recycle failed: " + err.Error()
				}
			})
		}
		imgui.EndDisabled()
	}

	if imgui.CollapsingHeaderBoolPtrV("Departures", nil, imgui.TreeNodeFlagsDefaultOpen) {
		flags, size := tableSize(len(ss.DepartureLaunchSlots), 10)
		if imgui.BeginTableV("deps", 6, flags, size, 0) {
			imgui.TableSetupColumn("Airport")
			imgui.TableSetupColumn("Runway")
			imgui.TableSetupColumn("Exit")
			imgui.TableSetupColumn("Type")
			imgui.TableSetupColumn("Callsign")
			imgui.TableSetupColumn("")
			imgui.TableHeadersRow()

			for i, slot := range ss.DepartureLaunchSlots {
				imgui.PushIDInt(int32(i))
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(string(slot.Airport))
				imgui.TableNextColumn()
				imgui.Text(string(slot.Runway))
				imgui.TableNextColumn()
				imgui.Text(slot.Exit)
				imgui.TableNextColumn()
				imgui.Text(slot.AircraftType)
				imgui.TableNextColumn()
				imgui.Text(string(slot.Callsign))
				imgui.TableNextColumn()
				if slot.Callsign != "" {
					buttons(slot.LaunchFlight)
				}
				imgui.PopID()
			}
			imgui.EndTable()
		}
	}

	if imgui.CollapsingHeaderBoolPtrV("Arrivals and overflights", nil, imgui.TreeNodeFlagsDefaultOpen) {
		flags, size := tableSize(len(ss.InboundLaunchSlots), 10)
		if imgui.BeginTableV("inbound", 5, flags, size, 0) {
			imgui.TableSetupColumn("Flow")
			imgui.TableSetupColumn("Airport")
			imgui.TableSetupColumn("Type")
			imgui.TableSetupColumn("Callsign")
			imgui.TableSetupColumn("")
			imgui.TableHeadersRow()

			for i, slot := range ss.InboundLaunchSlots {
				imgui.PushIDInt(int32(i))
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(slot.Group)
				imgui.TableNextColumn()
				imgui.Text(string(slot.Airport))
				imgui.TableNextColumn()
				imgui.Text(slot.AircraftType)
				imgui.TableNextColumn()
				imgui.Text(string(slot.Callsign))
				imgui.TableNextColumn()
				if slot.Callsign != "" {
					buttons(slot.LaunchFlight)
				}
				imgui.PopID()
			}
			imgui.EndTable()
		}
	}
}

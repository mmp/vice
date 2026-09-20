// cmd/vice/launchrates.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"runtime"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// drawPublishedDepartureUI and drawPublishedArrivalUI stand in for the rate
// controls when the sim is flying historical or timetable traffic: how much
// there is and where it goes comes from the data, so what is left to choose is
// how fast to fly it and which flows are active.
func drawPublishedDepartureUI(lc *sim.LaunchConfig, p platform.Platform) (changed bool) {
	label := util.Select(lc.TrafficSource == sim.TrafficSourceHistorical,
		"Historical departure rate scale", "Timetable departure rate scale")
	imgui.SetNextItemWidth(260)
	changed = imgui.SliderFloatV(label, &lc.PublishedDepartureRateScale, 0, sim.MaxPublishedRateScale,
		"%.1f", imgui.SliderFlagsNoInput)

	airportDepartures := lc.WorkedDepartureCounts() // key is e.g. KJFK, then count of runways cross categories.
	maxDepartureCategories := 0
	for _, n := range airportDepartures {
		maxDepartureCategories = max(n, maxDepartureCategories)
	}
	if maxDepartureCategories == 0 {
		return
	}

	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
	adrColumns := min(3, maxDepartureCategories)
	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	if imgui.BeginTableV("departureRunways", int32(1+3*adrColumns), flags, imgui.Vec2{tableScale * float32(200+250*adrColumns), 0}, 0.) {
		imgui.TableSetupColumn("Airport")
		for range adrColumns {
			imgui.TableSetupColumn("Runway")
			imgui.TableSetupColumn("Category")
			imgui.TableSetupColumn("Active")
		}
		imgui.TableHeadersRow()

		for airport := range util.SortedMap(airportDepartures) {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text(string(airport))

			imgui.PushIDStr(string(airport))
			adrColumn := 0
			for runway := range util.SortedMap(lc.DepartureEnabled[airport]) {
				imgui.PushIDStr(string(runway))

				for category := range util.SortedMap(lc.DepartureEnabled[airport][runway]) {
					if lc.DepartureIsBackground(airport, runway, category) {
						continue
					}

					imgui.TableNextColumn()
					imgui.Text(runway.Base()) // don't include extras in the UI
					imgui.TableNextColumn()

					imgui.PushIDStr(category)

					if category == "" {
						imgui.Text("(All)")
					} else {
						imgui.Text(category)
					}
					imgui.TableNextColumn()

					enabled := lc.DepartureEnabled[airport][runway][category]
					if imgui.Checkbox("##enabled", &enabled) {
						lc.DepartureEnabled[airport][runway][category] = enabled
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

	imgui.Separator()

	return
}

func drawPublishedArrivalUI(lc *sim.LaunchConfig, p platform.Platform) (changed bool) {
	label := util.Select(lc.TrafficSource == sim.TrafficSourceHistorical,
		"Historical arrival rate scale", "Timetable arrival rate scale")
	imgui.SetNextItemWidth(260)
	changed = imgui.SliderFloatV(label, &lc.PublishedArrivalRateScale, 0, sim.MaxPublishedRateScale,
		"%.1f", imgui.SliderFlagsNoInput)

	// Go-arounds still apply; the arrival pushes don't, since when aircraft
	// show up is what the data says.
	changed = imgui.SliderFloatV("Go around probability", &lc.GoAroundRate, 0, 1, "%.02f", 0) || changed

	numAirportFlows := lc.WorkedInboundFlowCounts()
	if len(numAirportFlows) == 0 { // no arrivals
		return
	}
	maxAirportFlows := 0
	for _, n := range numAirportFlows {
		maxAirportFlows = max(n, maxAirportFlows)
	}

	aarColumns := min(3, maxAirportFlows)
	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	if imgui.BeginTableV("arrivalgroups", int32(1+2*aarColumns), flags, imgui.Vec2{tableScale * float32(150+250*aarColumns), 0}, 0.) {
		imgui.TableSetupColumn("Airport")
		for range aarColumns {
			imgui.TableSetupColumn("Arrival")
			imgui.TableSetupColumn("Active")
		}
		imgui.TableHeadersRow()

		for ap := range util.SortedMap(numAirportFlows) {
			imgui.PushIDStr(string(ap))
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text(string(ap))

			aarCol := 0
			for group, apEnabled := range util.SortedMap(lc.InboundFlowEnabled) {
				imgui.PushIDStr(group)
				if enabled, ok := apEnabled[string(ap)]; ok && !lc.InboundFlowIsBackground(group, string(ap)) {
					if aarCol > 0 && aarCol%aarColumns == 0 {
						// Overflow
						imgui.TableNextRow()
						imgui.TableNextColumn()
					}

					imgui.TableNextColumn()
					imgui.Text(group)
					imgui.TableNextColumn()
					if imgui.Checkbox("##enabled", &enabled) {
						lc.InboundFlowEnabled[group][string(ap)] = enabled
						changed = true
					}
					aarCol++
				}
				imgui.PopID()
			}
			imgui.PopID()
		}
		imgui.EndTable()
	}

	imgui.Separator()

	return
}

func drawDepartureUI(lc *sim.LaunchConfig, p platform.Platform) (changed bool) {
	if lc.TrafficSource != sim.TrafficSourceScenario {
		return drawPublishedDepartureUI(lc, p)
	}

	airportDepartures := lc.WorkedDepartureCounts() // key is e.g. KJFK, then count of active runways cross categories.
	if len(airportDepartures) == 0 {
		return
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

		for airport := range util.SortedMap(airportDepartures) {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text(string(airport))

			imgui.PushIDStr(string(airport))
			adrColumn := 0
			for runway := range util.SortedMap(lc.DepartureRates[airport]) {
				imgui.PushIDStr(string(runway))

				for category := range util.SortedMap(lc.DepartureRates[airport][runway]) {
					if lc.DepartureIsBackground(airport, runway, category) {
						continue
					}

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

					r := lc.DepartureRateScale * lc.DepartureRates[airport][runway][category]
					if imgui.InputFloatV("##adr", &r, 0, 0, "%g", 0) {
						lc.DepartureRates[airport][runway][category] = r / max(.01, lc.DepartureRateScale)
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
	numAirportFlows := lc.WorkedInboundFlowCounts()
	if len(numAirportFlows) == 0 { // no arrivals
		return
	}
	if lc.TrafficSource != sim.TrafficSourceScenario {
		return drawPublishedArrivalUI(lc, p)
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
			imgui.PushIDStr(string(ap))
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text(string(ap))

			aarCol := 0
			for group, aprates := range util.SortedMap(lc.InboundFlowRates) {
				imgui.PushIDStr(group)
				if rate, ok := aprates[string(ap)]; ok && !lc.InboundFlowIsBackground(group, string(ap)) {
					if aarCol > 0 && aarCol%aarColumns == 0 {
						// Overflow
						imgui.TableNextRow()
						imgui.TableNextColumn()
					}

					imgui.TableNextColumn()
					imgui.Text(group)
					imgui.TableNextColumn()
					r := rate * lc.InboundFlowRateScale
					if imgui.InputFloatV("##aar-"+string(ap), &r, 0, 0, "%g", 0) {
						changed = true
						lc.InboundFlowRates[group][string(ap)] = r / max(.01, lc.InboundFlowRateScale)
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
	overflightGroups := lc.WorkedOverflightGroups()
	if len(overflightGroups) == 0 {
		return
	}

	if lc.TrafficSource != sim.TrafficSourceScenario {
		imgui.TextDisabled("Overflights are randomly generated; these rates apply with any traffic source.")
	}

	// The arrivals section carries the scale that applies to all inbound
	// traffic, but a scenario with no arrivals to work doesn't have one.
	if !lc.HaveWorkedArrivals() {
		changed = imgui.SliderFloatV("Overflight rate scale", &lc.InboundFlowRateScale, 0, 5, "%.1f",
			imgui.SliderFlagsNoInput) || changed
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
		for _, group := range overflightGroups {
			imgui.PushIDStr(group)
			if ofCol%ofColumns == 0 {
				imgui.TableNextRow()
			}

			imgui.TableNextColumn()
			imgui.Text(group)
			imgui.TableNextColumn()
			r := lc.InboundFlowRates[group]["overflights"] * lc.InboundFlowRateScale
			if imgui.InputFloatV("##of-"+group, &r, 0, 0, "%g", 0) {
				changed = true
				lc.InboundFlowRates[group]["overflights"] = r / max(.01, lc.InboundFlowRateScale)
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

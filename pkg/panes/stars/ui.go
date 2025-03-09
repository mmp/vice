// ui.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"

	"github.com/mmp/imgui-go/v4"
)

func (sp *STARSPane) DrawUI(p platform.Platform, config *platform.Config) {
	ps := sp.currentPrefs()

	imgui.Text("Font: ")
	imgui.SameLine()
	imgui.RadioButtonInt("Default", &sp.FontSelection, fontDefault)
	imgui.SameLine()
	imgui.RadioButtonInt("Legacy", &sp.FontSelection, fontLegacy)
	imgui.SameLine()
	imgui.RadioButtonInt("ARTS", &sp.FontSelection, fontARTS)

	imgui.Checkbox("Auto track departures", &sp.AutoTrackDepartures)

	imgui.Checkbox("Lock display", &sp.LockDisplay)

	imgui.Checkbox("Invert numeric keypad", &sp.FlipNumericKeypad)

	if imgui.BeginComboV("TGT GEN Key", string(sp.TgtGenKey), imgui.ComboFlagsHeightLarge) {
		for _, key := range []byte{';', ','} {
			if imgui.SelectableV(string(key), key == sp.TgtGenKey, 0, imgui.Vec2{}) {
				sp.TgtGenKey = key
			}
		}
		imgui.EndCombo()
	}

	imgui.Separator()
	imgui.Text("Non-standard Audio Effects")

	// Only offer the non-standard ones to globally disable.
	for _, i := range []AudioType{AudioInboundHandoff, AudioHandoffAccepted} {
		imgui.Text("  ")
		imgui.SameLine()
		if imgui.Checkbox(AudioType(i).String(), &ps.AudioEffectEnabled[i]) && ps.AudioEffectEnabled[i] {
			sp.playOnce(p, i)
		}
	}
}

func (sp *STARSPane) DrawInfo(c *sim.ControlClient, p platform.Platform, lg *log.Logger) {
	// Make big(ish) tables somewhat more legible
	tableFlags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH |
		imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp

	if imgui.CollapsingHeader("Arrivals") {
		if imgui.BeginTableV("arr", 4, tableFlags, imgui.Vec2{}, 0) {
			if sp.scopeDraw.arrivals == nil {
				sp.scopeDraw.arrivals = make(map[string]map[int]bool)
			}

			imgui.TableSetupColumn("Draw")
			imgui.TableSetupColumn("Arrival")
			imgui.TableSetupColumn("Airport(s)")
			imgui.TableSetupColumn("Description")
			imgui.TableHeadersRow()

			for _, name := range util.SortedMapKeys(c.State.InboundFlows) {
				arrivals := c.State.InboundFlows[name].Arrivals
				if len(arrivals) == 0 {
					continue
				}
				if sp.scopeDraw.arrivals[name] == nil {
					sp.scopeDraw.arrivals[name] = make(map[int]bool)
				}

				for i, arr := range arrivals {
					if len(c.State.LaunchConfig.InboundFlowRates[name]) == 0 {
						// Not used in the current scenario.
						continue
					}

					imgui.TableNextRow()
					imgui.TableNextColumn()
					enabled := sp.scopeDraw.arrivals[name][i]
					imgui.Checkbox(fmt.Sprintf("##arr-%s-%d", name, i), &enabled)
					sp.scopeDraw.arrivals[name][i] = enabled

					imgui.TableNextColumn()
					imgui.Text(name)

					imgui.TableNextColumn()
					airports := util.SortedMapKeys(arr.Airlines)
					imgui.Text(strings.Join(airports, ", "))

					imgui.TableNextColumn()
					if arr.Description != "" {
						imgui.Text(arr.Description)
					} else {
						imgui.Text("--")
					}
				}
			}

			imgui.EndTable()
		}
	}

	if imgui.CollapsingHeader("Approaches") {
		if imgui.BeginTableV("appr", 6, tableFlags, imgui.Vec2{}, 0) {
			if sp.scopeDraw.approaches == nil {
				sp.scopeDraw.approaches = make(map[string]map[string]bool)
			}

			imgui.TableSetupColumn("Draw")
			imgui.TableSetupColumn("Airport")
			imgui.TableSetupColumn("Runway")
			imgui.TableSetupColumn("Code")
			imgui.TableSetupColumn("Description")
			imgui.TableSetupColumn("FAF")
			imgui.TableHeadersRow()

			for _, rwy := range c.State.ArrivalRunways {
				if ap, ok := c.State.Airports[rwy.Airport]; !ok {
					lg.Errorf("%s: arrival airport not in world airports", rwy.Airport)
				} else {
					if sp.scopeDraw.approaches[rwy.Airport] == nil {
						sp.scopeDraw.approaches[rwy.Airport] = make(map[string]bool)
					}
					for _, name := range util.SortedMapKeys(ap.Approaches) {
						appr := ap.Approaches[name]
						if appr.Runway == rwy.Runway {
							imgui.TableNextRow()
							imgui.TableNextColumn()
							enabled := sp.scopeDraw.approaches[rwy.Airport][name]
							imgui.Checkbox("##enable-"+rwy.Airport+"-"+rwy.Runway+"-"+name, &enabled)
							sp.scopeDraw.approaches[rwy.Airport][name] = enabled

							imgui.TableNextColumn()
							imgui.Text(rwy.Airport)

							imgui.TableNextColumn()
							imgui.Text(rwy.Runway)

							imgui.TableNextColumn()
							imgui.Text(name)

							imgui.TableNextColumn()
							imgui.Text(appr.FullName)

							imgui.TableNextColumn()
							for _, wp := range appr.Waypoints[0] {
								if wp.FAF {
									imgui.Text(wp.Fix)
									break
								}
							}
						}
					}
				}
			}
			imgui.EndTable()
		}
	}

	if imgui.CollapsingHeader("Departures") {
		if imgui.BeginTableV("departures", 5, tableFlags, imgui.Vec2{}, 0) {
			if sp.scopeDraw.departures == nil {
				sp.scopeDraw.departures = make(map[string]map[string]map[string]bool)
			}

			imgui.TableSetupColumn("Draw")
			imgui.TableSetupColumn("Airport")
			imgui.TableSetupColumn("Runway")
			imgui.TableSetupColumn("Exit")
			imgui.TableSetupColumn("Description")
			imgui.TableHeadersRow()

			for _, airport := range util.SortedMapKeys(c.State.LaunchConfig.DepartureRates) {
				if sp.scopeDraw.departures[airport] == nil {
					sp.scopeDraw.departures[airport] = make(map[string]map[string]bool)
				}
				ap := c.State.Airports[airport]

				runwayRates := c.State.LaunchConfig.DepartureRates[airport]
				for _, rwy := range util.SortedMapKeys(runwayRates) {
					if sp.scopeDraw.departures[airport][rwy] == nil {
						sp.scopeDraw.departures[airport][rwy] = make(map[string]bool)
					}

					exitRoutes := ap.DepartureRoutes[rwy]

					// Multiple routes may have the same waypoints, so
					// we'll reverse-engineer that here so we can present
					// them together in the UI.
					routeToExit := make(map[string][]string)
					for _, exit := range util.SortedMapKeys(exitRoutes) {
						exitRoute := ap.DepartureRoutes[rwy][exit]
						r := exitRoute.Waypoints.Encode()
						routeToExit[r] = append(routeToExit[r], exit)
					}

					for _, exit := range util.SortedMapKeys(exitRoutes) {
						// Draw the row only when we hit the first exit
						// that uses the corresponding route route.
						r := exitRoutes[exit].Waypoints.Encode()
						if routeToExit[r][0] != exit {
							continue
						}

						imgui.TableNextRow()
						imgui.TableNextColumn()
						enabled := sp.scopeDraw.departures[airport][rwy][exit]
						imgui.Checkbox("##enable-"+airport+"-"+rwy+"-"+exit, &enabled)
						sp.scopeDraw.departures[airport][rwy][exit] = enabled

						imgui.TableNextColumn()
						imgui.Text(airport)
						imgui.TableNextColumn()
						rwyBase, _, _ := strings.Cut(rwy, ".")
						imgui.Text(rwyBase)
						imgui.TableNextColumn()
						if len(routeToExit) == 1 {
							// If we only saw a single departure route, no
							// need to list all of the exits in the UI
							// (there are often a lot of them!)
							imgui.Text("(all)")
						} else {
							// List all of the exits that use this route.
							imgui.Text(strings.Join(routeToExit[r], ", "))
						}
						imgui.TableNextColumn()
						imgui.Text(exitRoutes[exit].Description)
					}
				}
			}
			imgui.EndTable()
		}
	}

	if imgui.CollapsingHeader("Overflights") {
		if imgui.BeginTableV("over", 3, tableFlags, imgui.Vec2{}, 0) {
			if sp.scopeDraw.overflights == nil {
				sp.scopeDraw.overflights = make(map[string]map[int]bool)
			}

			imgui.TableSetupColumn("Draw")
			imgui.TableSetupColumn("Overflight")
			imgui.TableSetupColumn("Description")
			imgui.TableHeadersRow()

			for _, name := range util.SortedMapKeys(c.State.InboundFlows) {
				overflights := c.State.InboundFlows[name].Overflights
				if len(overflights) == 0 {
					continue
				}

				if sp.scopeDraw.overflights[name] == nil {
					sp.scopeDraw.overflights[name] = make(map[int]bool)
				}
				if _, ok := c.State.LaunchConfig.InboundFlowRates[name]["overflights"]; !ok {
					// Not used in the current scenario.
					continue
				}

				for i, of := range overflights {
					imgui.TableNextRow()
					imgui.TableNextColumn()
					enabled := sp.scopeDraw.overflights[name][i]
					imgui.Checkbox(fmt.Sprintf("##of-%s-%d", name, i), &enabled)
					sp.scopeDraw.overflights[name][i] = enabled

					imgui.TableNextColumn()
					imgui.Text(name)

					imgui.TableNextColumn()
					if of.Description != "" {
						imgui.Text(of.Description)
					} else {
						imgui.Text("--")
					}
				}
			}

			imgui.EndTable()
		}
	}

	if len(c.State.Airspace) > 0 && imgui.CollapsingHeader("Airspace") {
		if sp.scopeDraw.airspace == nil {
			sp.scopeDraw.airspace = make(map[string]map[string]bool)
			for ctrl, sectors := range c.State.Airspace {
				sp.scopeDraw.airspace[ctrl] = make(map[string]bool)
				for _, sector := range util.SortedMapKeys(sectors) {
					sp.scopeDraw.airspace[ctrl][sector] = false
				}
			}
		}
		for _, pos := range util.SortedMapKeys(sp.scopeDraw.airspace) {
			hdr := pos
			if ctrl, ok := c.State.Controllers[pos]; ok {
				hdr += " (" + ctrl.Position + ")"
			}
			if imgui.TreeNode(hdr) {
				if imgui.BeginTableV("volumes", 2, tableFlags, imgui.Vec2{}, 0) {
					for _, vol := range util.SortedMapKeys(sp.scopeDraw.airspace[pos]) {
						imgui.TableNextRow()
						imgui.TableNextColumn()
						b := sp.scopeDraw.airspace[pos][vol]
						if imgui.Checkbox("##"+vol, &b) {
							sp.scopeDraw.airspace[pos][vol] = b
						}
						imgui.TableNextColumn()
						imgui.Text(vol)
					}

					imgui.EndTable()
				}
				imgui.TreePop()
			}
		}
	}

	if imgui.CollapsingHeader("Tower/Coordination Lists") {
		if imgui.BeginTableV("tclists", 3, tableFlags, imgui.Vec2{}, 0) {
			imgui.TableSetupColumn("Id")
			imgui.TableSetupColumn("Type")
			imgui.TableSetupColumn("Airports")
			imgui.TableHeadersRow()

			for i, ap := range c.TowerListAirports() {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(strconv.Itoa(i + 1))
				imgui.TableNextColumn()
				imgui.Text("Tower")
				imgui.TableNextColumn()
				imgui.Text(ap)
			}

			cl := util.DuplicateSlice(c.State.STARSFacilityAdaptation.CoordinationLists)
			slices.SortFunc(cl, func(a, b sim.CoordinationList) int { return strings.Compare(a.Id, b.Id) })

			for _, list := range cl {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(list.Id)
				imgui.TableNextColumn()
				imgui.Text("Coord. (" + list.Name + ")")
				imgui.TableNextColumn()
				imgui.Text(strings.Join(list.Airports, ", "))
			}
			imgui.EndTable()
		}
	}

	if aa := c.State.STARSFacilityAdaptation.AirspaceAwareness; len(aa) > 0 {
		if imgui.CollapsingHeader("Airspace Awareness") {
			if imgui.BeginTableV("awareness", 4, tableFlags, imgui.Vec2{}, 0) {
				imgui.TableSetupColumn("Fix")
				imgui.TableSetupColumn("Altitude")
				imgui.TableSetupColumn("A/C Type")
				imgui.TableSetupColumn("Controller")
				imgui.TableHeadersRow()

				for _, aware := range aa {
					for _, fix := range aware.Fix {
						imgui.TableNextRow()
						imgui.TableNextColumn()
						imgui.Text(fix)
						imgui.TableNextColumn()
						alt := ""
						if aware.AltitudeRange[0] > 0 {
							if aware.AltitudeRange[1] < 60000 {
								alt = av.FormatAltitude(float32(aware.AltitudeRange[0])) + " - " +
									av.FormatAltitude(float32(aware.AltitudeRange[1]))
							} else {
								alt = av.FormatAltitude(float32(aware.AltitudeRange[0])) + "+"
							}
						} else if aware.AltitudeRange[1] < 60000 {
							alt = av.FormatAltitude(float32(aware.AltitudeRange[1])) + "-"
						}
						imgui.Text(alt)
						imgui.TableNextColumn()
						imgui.Text(strings.Join(aware.AircraftType, ", "))
						imgui.TableNextColumn()
						imgui.Text(aware.ReceivingController)
					}
				}

				imgui.EndTable()
			}
		}
	}
}

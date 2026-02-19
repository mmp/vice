package eram

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/AllenDang/cimgui-go/imgui"
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

var _ panes.UIDrawer = (*ERAMPane)(nil)

func (ep *ERAMPane) DisplayName() string { return "ERAM" }

func (ep *ERAMPane) DrawUI(p platform.Platform, config *platform.Config) {
	imgui.Checkbox("Disable ERAM to Radio Commands", &ep.DisableERAMtoRadio)
	imgui.Checkbox("Invert numeric keypad", &ep.FlipNumericKeypad)
	if ep.prefSet == nil {
		return
	}
	ps := ep.currentPrefs()
	imgui.Checkbox("Use right click for primary button", &ps.UseRightClick)
	tableFlags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH |
		imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
	if imgui.CollapsingHeaderBoolPtr("Preferences", nil) {
		if imgui.BeginTableV("Saved Preferences", 4, tableFlags, imgui.Vec2{}, 0) {
			imgui.TableSetupColumn("Name")
			imgui.TableSetupColumn("Save ")
			imgui.TableSetupColumn("Load ")
			imgui.TableSetupColumn("Delete ")
			imgui.TableHeadersRow()
			// Only show rows that match current ARTCC and map group
			currentARTCC := ep.currentFacility
			currentGroup := ep.prefSet.Current.VideoMapGroup
			saved := ep.prefSet.Saved[:]
			for i, pref := range saved {
				if pref == nil {
					// Keep nil until user saves; we'll use tempSavedNames[i] for input binding
				}
				// Ensure all widgets in this row have unique IDs by pushing a per-row ID
				imgui.PushIDInt(int32(i))
				imgui.TableNextRow()
				imgui.TableNextColumn()
				// If slot contains a pref for a different ARTCC/group, hide its name
				existingName := ""
				if pref != nil && pref.ARTCC == currentARTCC && pref.VideoMapGroup == currentGroup {
					existingName = pref.Name
				}
				// Bind to a stable per-row temp string; show existing name as hint
				imgui.InputTextWithHint("##name", existingName, &ep.tempSavedNames[i], imgui.InputTextFlagsNone, nil)
				imgui.TableNextColumn()
				if imgui.Button("Save") {
					// Determine the name to save under
					saveName := strings.TrimSpace(ep.tempSavedNames[i])
					if saveName == "" && pref != nil {
						saveName = pref.Name
					}
					if saveName != "" { // Only save when we have a non-empty name
						// Copy current preferences into this slot and set the saved name
						cp := ep.prefSet.Current
						// Store plain name; scope via ARTCC and VideoMapGroup fields
						cp.Name = saveName
						cp.ARTCC = currentARTCC
						// Deep copy map fields so saved prefs are not mutated later
						if cp.VideoMapVisible != nil {
							cp.VideoMapVisible = cloneStringAnyMap(cp.VideoMapVisible)
						}
						if cp.VideoMapBrightness != nil {
							cp.VideoMapBrightness = cloneStringIntMap(cp.VideoMapBrightness)
						}
						ep.prefSet.Saved[i] = &cp
						ep.tempSavedNames[i] = ""
					}
				}
				imgui.TableNextColumn()
				if imgui.Button("Load") {
					if pref != nil && pref.ARTCC == currentARTCC && pref.VideoMapGroup == currentGroup {
						ep.prefSet.Current = *pref
						// Clone map fields so editing current doesn't mutate saved copy
						if pref.VideoMapVisible != nil {
							ep.prefSet.Current.VideoMapVisible = cloneStringAnyMap(pref.VideoMapVisible)
						}
						if pref.VideoMapBrightness != nil {
							ep.prefSet.Current.VideoMapBrightness = cloneStringIntMap(pref.VideoMapBrightness)
						}
					}
				}
				imgui.TableNextColumn()
				if imgui.Button("Delete") {
					ep.prefSet.Saved[i] = nil
					ep.tempSavedNames[i] = ""
				}
				imgui.PopID()
			}
			imgui.EndTable()
		}
	}
}

func (ep *ERAMPane) DrawInfo(c *client.ControlClient, p platform.Platform, lg *log.Logger) {
	// Make big(ish) tables somewhat more legible
	tableFlags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH |
		imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp

	if imgui.CollapsingHeaderBoolPtr("Arrivals", nil) {
		imgui.Text("Color:")
		imgui.SameLine()
		imgui.ColorEdit3V("Draw Color##1", ep.IFPHelpers.ArrivalsColor, imgui.ColorEditFlagsNoInputs|imgui.ColorEditFlagsNoLabel)

		if imgui.BeginTableV("arr", 4, tableFlags, imgui.Vec2{}, 0) {
			if ep.scopeDraw.arrivals == nil {
				ep.scopeDraw.arrivals = make(map[string]map[int]bool)
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
				if ep.scopeDraw.arrivals[name] == nil {
					ep.scopeDraw.arrivals[name] = make(map[int]bool)
				}

				for i, arr := range arrivals {
					if len(c.State.LaunchConfig.InboundFlowRates[name]) == 0 {
						// Not used in the current scenario.
						continue
					}

					imgui.TableNextRow()
					imgui.TableNextColumn()
					enabled := ep.scopeDraw.arrivals[name][i]
					imgui.Checkbox(fmt.Sprintf("##arr-%s-%d", name, i), &enabled)
					ep.scopeDraw.arrivals[name][i] = enabled

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

	if imgui.CollapsingHeaderBoolPtr("Approaches", nil) {
		imgui.Text("Color:")
		imgui.SameLine()
		imgui.ColorEdit3V("Draw Color##2", ep.IFPHelpers.ApproachesColor, imgui.ColorEditFlagsNoInputs|imgui.ColorEditFlagsNoLabel)

		if imgui.BeginTableV("appr", 6, tableFlags, imgui.Vec2{}, 0) {

			if ep.scopeDraw.approaches == nil {
				ep.scopeDraw.approaches = make(map[string]map[string]bool)
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
					if ep.scopeDraw.approaches[rwy.Airport] == nil {
						ep.scopeDraw.approaches[rwy.Airport] = make(map[string]bool)
					}
					for _, name := range util.SortedMapKeys(ap.Approaches) {
						appr := ap.Approaches[name]
						if appr.Runway == rwy.Runway.Base() {
							imgui.TableNextRow()
							imgui.TableNextColumn()
							enabled := ep.scopeDraw.approaches[rwy.Airport][name]
							imgui.Checkbox("##enable-"+rwy.Airport+"-"+string(rwy.Runway)+"-"+name, &enabled)
							ep.scopeDraw.approaches[rwy.Airport][name] = enabled

							imgui.TableNextColumn()
							imgui.Text(rwy.Airport)

							imgui.TableNextColumn()
							imgui.Text(string(rwy.Runway))

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

	if imgui.CollapsingHeaderBoolPtr("Departures", nil) {
		imgui.Text("Color:")
		imgui.SameLine()
		imgui.ColorEdit3V("Draw Color##3", ep.IFPHelpers.DeparturesColor, imgui.ColorEditFlagsNoInputs|imgui.ColorEditFlagsNoLabel)

		if imgui.BeginTableV("departures", 5, tableFlags, imgui.Vec2{}, 0) {
			if ep.scopeDraw.departures == nil {
				ep.scopeDraw.departures = make(map[string]map[string]map[string]bool)
			}

			imgui.TableSetupColumn("Draw")
			imgui.TableSetupColumn("Airport")
			imgui.TableSetupColumn("Runway")
			imgui.TableSetupColumn("Exit")
			imgui.TableSetupColumn("Description")
			imgui.TableHeadersRow()

			for _, airport := range util.SortedMapKeys(c.State.LaunchConfig.DepartureRates) {
				if ep.scopeDraw.departures[airport] == nil {
					ep.scopeDraw.departures[airport] = make(map[string]map[string]bool)
				}
				ap := c.State.Airports[airport]

				runwayRates := c.State.LaunchConfig.DepartureRates[airport]
				for _, rwy := range util.SortedMapKeys(runwayRates) {
					if ep.scopeDraw.departures[airport][string(rwy)] == nil {
						ep.scopeDraw.departures[airport][string(rwy)] = make(map[string]bool)
					}

					exitRoutes := ap.DepartureRoutes[rwy]

					// Multiple routes may have the same waypoints, so
					// we'll reverse-engineer that here so we can present
					// them together in the UI.
					routeToExit := make(map[string][]string)
					for _, exit := range util.SortedMapKeys(exitRoutes) {
						exitRoute := ap.DepartureRoutes[rwy][exit]
						r := exitRoute.Waypoints.Encode()
						routeToExit[r] = append(routeToExit[r], string(exit))
					}

					for _, exit := range util.SortedMapKeys(exitRoutes) {
						// Draw the row only when we hit the first exit
						// that uses the corresponding route route.
						r := exitRoutes[exit].Waypoints.Encode()
						if routeToExit[r][0] != string(exit) {
							continue
						}

						imgui.TableNextRow()
						imgui.TableNextColumn()
						enabled := ep.scopeDraw.departures[airport][string(rwy)][string(exit)]
						imgui.Checkbox("##enable-"+airport+"-"+string(rwy)+"-"+string(exit), &enabled)
						ep.scopeDraw.departures[airport][string(rwy)][string(exit)] = enabled

						imgui.TableNextColumn()
						imgui.Text(airport)
						imgui.TableNextColumn()
						rwyBase := rwy.Base()
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

	if imgui.CollapsingHeaderBoolPtr("Overflights", nil) {
		imgui.Text("Color:")
		imgui.SameLine()
		imgui.ColorEdit3V("Draw Color##4", ep.IFPHelpers.OverflightsColor, imgui.ColorEditFlagsNoInputs|imgui.ColorEditFlagsNoLabel)

		if imgui.BeginTableV("over", 3, tableFlags, imgui.Vec2{}, 0) {
			if ep.scopeDraw.overflights == nil {
				ep.scopeDraw.overflights = make(map[string]map[int]bool)
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

				if ep.scopeDraw.overflights[name] == nil {
					ep.scopeDraw.overflights[name] = make(map[int]bool)
				}
				if _, ok := c.State.LaunchConfig.InboundFlowRates[name]["overflights"]; !ok {
					// Not used in the current scenario.
					continue
				}

				for i, of := range overflights {
					imgui.TableNextRow()
					imgui.TableNextColumn()
					enabled := ep.scopeDraw.overflights[name][i]
					imgui.Checkbox(fmt.Sprintf("##of-%s-%d", name, i), &enabled)
					ep.scopeDraw.overflights[name][i] = enabled

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

	if len(c.State.Airspace) > 0 && imgui.CollapsingHeaderBoolPtr("Airspace", nil) {
		imgui.Text("Color:")
		imgui.SameLine()
		imgui.ColorEdit3V("Draw Color##5", ep.IFPHelpers.AirspaceColor, imgui.ColorEditFlagsNoInputs|imgui.ColorEditFlagsNoLabel)

		if ep.scopeDraw.airspace == nil {
			ep.scopeDraw.airspace = make(map[sim.ControlPosition]map[string]bool)
			for ctrl, sectors := range c.State.Airspace {
				ep.scopeDraw.airspace[ctrl] = make(map[string]bool)
				for _, sector := range util.SortedMapKeys(sectors) {
					ep.scopeDraw.airspace[ctrl][sector] = false
				}
			}
		}
		for _, pos := range util.SortedMapKeys(ep.scopeDraw.airspace) {
			hdr := string(pos)
			if ctrl, ok := c.State.Controllers[pos]; ok {
				hdr += " (" + ctrl.Position + ")"
			}
			if imgui.TreeNodeExStr(hdr) {
				if imgui.BeginTableV("volumes", 2, tableFlags, imgui.Vec2{}, 0) {
					for _, vol := range util.SortedMapKeys(ep.scopeDraw.airspace[pos]) {
						imgui.TableNextRow()
						imgui.TableNextColumn()
						b := ep.scopeDraw.airspace[pos][vol]
						if imgui.Checkbox("##"+vol, &b) {
							ep.scopeDraw.airspace[pos][vol] = b
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

	if imgui.CollapsingHeaderBoolPtr("Tower/Coordination Lists", nil) {
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

			cl := util.DuplicateSlice(c.State.FacilityAdaptation.CoordinationLists)
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

	if aa := c.State.FacilityAdaptation.AirspaceAwareness; len(aa) > 0 {
		if imgui.CollapsingHeaderBoolPtr("Airspace Awareness", nil) {
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

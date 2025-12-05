// ui.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

var _ panes.UIDrawer = (*STARSPane)(nil)

func (sp *STARSPane) DisplayName() string { return "STARS" }

func (sp *STARSPane) DrawUI(p platform.Platform, config *platform.Config) {
	imgui.Text("Font: ")
	imgui.SameLine()
	imgui.RadioButtonIntPtr("Default", &sp.FontSelection, fontDefault)
	imgui.SameLine()
	imgui.RadioButtonIntPtr("Legacy", &sp.FontSelection, fontLegacy)
	imgui.SameLine()
	imgui.RadioButtonIntPtr("ARTS", &sp.FontSelection, fontARTS)

	imgui.Checkbox("Lock display", &sp.LockDisplay)

	imgui.Checkbox("Invert numeric keypad", &sp.FlipNumericKeypad)

	if imgui.BeginComboV("TGT GEN Key", string(sp.TgtGenKey), imgui.ComboFlagsHeightLarge) {
		for _, key := range []byte{';', ','} {
			if imgui.SelectableBoolV(string(key), key == sp.TgtGenKey, 0, imgui.Vec2{}) {
				sp.TgtGenKey = key
			}
		}
		imgui.EndCombo()
	}

	if sp.prefSet != nil { // Hacky workaround to crash if DrawUI runs with no active STARS Pane.
		imgui.Separator()
		imgui.Text("Non-standard Audio Effects")

		ps := sp.currentPrefs()
		// Only offer the non-standard ones to globally disable.
		for _, i := range []AudioType{AudioInboundHandoff, AudioHandoffAccepted} {
			imgui.Text("  ")
			imgui.SameLine()
			if imgui.Checkbox(AudioType(i).String(), &ps.AudioEffectEnabled[i]) && ps.AudioEffectEnabled[i] {
				sp.playOnce(p, i)
			}
		}
	}
}

func (sp *STARSPane) DrawInfo(c *client.ControlClient, p platform.Platform, lg *log.Logger) {
	// Make big(ish) tables somewhat more legible
	tableFlags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH |
		imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp

	if imgui.CollapsingHeaderBoolPtr("Arrivals", nil) {
		imgui.Text("Color:")
		imgui.SameLine()
		imgui.ColorEdit3V("Draw Color##1", sp.IFPHelpers.ArrivalsColor, imgui.ColorEditFlagsNoInputs|imgui.ColorEditFlagsNoLabel)

		if imgui.BeginTableV("arr", 4, tableFlags, imgui.Vec2{}, 0) {
			if sp.scopeDraw.arrivals == nil {
				sp.scopeDraw.arrivals = make(map[string]map[int]bool)
			}

			imgui.TableSetupColumn("Draw")
			imgui.TableSetupColumn("Arrival")
			imgui.TableSetupColumn("Airport(s)")
			imgui.TableSetupColumn("Description")
			imgui.TableHeadersRow()

			for name, flow := range util.SortedMap(c.State.InboundFlows) {
				arrivals := flow.Arrivals
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

	if imgui.CollapsingHeaderBoolPtr("Approaches", nil) {
		imgui.Text("Color:")
		imgui.SameLine()
		imgui.ColorEdit3V("Draw Color##2", sp.IFPHelpers.ApproachesColor, imgui.ColorEditFlagsNoInputs|imgui.ColorEditFlagsNoLabel)

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
					for name, appr := range util.SortedMap(ap.Approaches) {
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

	if imgui.CollapsingHeaderBoolPtr("Departures", nil) {
		imgui.Text("Color:")
		imgui.SameLine()
		imgui.ColorEdit3V("Draw Color##3", sp.IFPHelpers.DeparturesColor, imgui.ColorEditFlagsNoInputs|imgui.ColorEditFlagsNoLabel)

		if imgui.BeginTableV("departures", 6, tableFlags, imgui.Vec2{}, 0) {
			if sp.scopeDraw.departures == nil {
				sp.scopeDraw.departures = make(map[string]map[string]map[string]bool)
			}

			imgui.TableSetupColumn("Draw")
			imgui.TableSetupColumn("Airport")
			imgui.TableSetupColumn("SID")
			imgui.TableSetupColumn("Runways")
			imgui.TableSetupColumn("Exits")
			imgui.TableSetupColumn("Description")
			imgui.TableHeadersRow()

			for _, airport := range util.SortedMapKeys(c.State.LaunchConfig.DepartureRates) {
				if sp.scopeDraw.departures[airport] == nil {
					sp.scopeDraw.departures[airport] = make(map[string]map[string]bool)
				}
				ap := c.State.Airports[airport]
				runwayRates := c.State.LaunchConfig.DepartureRates[airport]

				// --- 1. Group by SID instead of runway ---
				sidGroups := make(map[string]struct {
					Runways      []string
					Exits        []string
					Descriptions []string
				})

				for _, rwy := range util.SortedMapKeys(runwayRates) {
					if sp.scopeDraw.departures[airport][rwy] == nil {
						sp.scopeDraw.departures[airport][rwy] = make(map[string]bool)
					}

					exitRoutes := ap.DepartureRoutes[rwy]
					for exit, exitRoute := range util.SortedMap(exitRoutes) {
						group := sidGroups[exitRoute.SID]
						if !slices.Contains(group.Runways, rwy) {
							group.Runways = append(group.Runways, rwy)
						}
						if !slices.Contains(group.Exits, exit) {
							group.Exits = append(group.Exits, exit)
						}
						if exitRoute.Description != "" && !slices.Contains(group.Descriptions, exitRoute.Description) {
							group.Descriptions = append(group.Descriptions, exitRoute.Description)
						}
						sidGroups[exitRoute.SID] = group
					}
				}

				// --- 2. Render each SID once ---
				for _, sid := range util.SortedMapKeys(sidGroups) {
					group := sidGroups[sid]

					imgui.TableNextRow()
					imgui.TableNextColumn()

					// Combine checkbox across runways (one toggle per SID)
					enabled := false
					for _, rwy := range group.Runways {
						for _, exit := range group.Exits {
							if sp.scopeDraw.departures[airport][rwy][exit] {
								enabled = true
							}
						}
					}
					imgui.Checkbox("##enable-"+airport+"-"+sid, &enabled)
					for _, rwy := range group.Runways {
						for _, exit := range group.Exits {
							sp.scopeDraw.departures[airport][rwy][exit] = enabled
						}
					}

					imgui.TableNextColumn()
					imgui.Text(airport)

					imgui.TableNextColumn()
					imgui.Text(sid)

					imgui.TableNextColumn()
					rwys := []string{}
					for _, r := range group.Runways {
						rwyBase, _, _ := strings.Cut(r, ".")
						rwys = append(rwys, rwyBase)
					}
					imgui.Text(strings.Join(rwys, ", "))

					imgui.TableNextColumn()
					imgui.Text(strings.Join(group.Exits, ", "))

					imgui.TableNextColumn()
					imgui.Text(strings.Join(group.Descriptions, " / "))
				}
			}

			imgui.EndTable()
		}
	}

	if imgui.CollapsingHeaderBoolPtr("Overflights", nil) {
		imgui.Text("Color:")
		imgui.SameLine()
		imgui.ColorEdit3V("Draw Color##4", sp.IFPHelpers.OverflightsColor, imgui.ColorEditFlagsNoInputs|imgui.ColorEditFlagsNoLabel)

		if imgui.BeginTableV("over", 3, tableFlags, imgui.Vec2{}, 0) {
			if sp.scopeDraw.overflights == nil {
				sp.scopeDraw.overflights = make(map[string]map[int]bool)
			}

			imgui.TableSetupColumn("Draw")
			imgui.TableSetupColumn("Overflight")
			imgui.TableSetupColumn("Description")
			imgui.TableHeadersRow()

			for name, flow := range util.SortedMap(c.State.InboundFlows) {
				overflights := flow.Overflights
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

	if len(c.State.Airspace) > 0 && imgui.CollapsingHeaderBoolPtr("Airspace", nil) {
		imgui.Text("Color:")
		imgui.SameLine()
		imgui.ColorEdit3V("Draw Color##5", sp.IFPHelpers.AirspaceColor, imgui.ColorEditFlagsNoInputs|imgui.ColorEditFlagsNoLabel)

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
			if imgui.TreeNodeExStr(hdr) {
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

	// Holds section - show enroute and unassociated holds within 75nm
	if imgui.CollapsingHeaderBoolPtr("Holds", nil) {
		imgui.Text("Color:")
		imgui.SameLine()
		imgui.ColorEdit3V("Draw Color##6", sp.IFPHelpers.HoldsColor, imgui.ColorEditFlagsNoInputs|imgui.ColorEditFlagsNoLabel)

		candidateHolds := make(map[string]av.Hold)
		ps := sp.currentPrefs()
		ctr := util.Select(ps.UseUserCenter, ps.UserCenter, ps.DefaultCenter)
		for fix, holds := range util.SortedMap(av.DB.EnrouteHolds) {
			loc, _ := av.DB.LookupWaypoint(fix)
			if dist := math.NMDistance2LL(ctr, loc); dist <= ps.Range {
				for _, h := range holds {
					// Only show holds that aren't part of procedures
					// (holds with Procedure set are drawn with their procedures)
					if h.Procedure == "" {
						candidateHolds[h.DisplayName()] = h
					}
				}
			}
		}

		if imgui.Checkbox("Draw all holds", &sp.scopeDraw.allHolds) && !sp.scopeDraw.allHolds {
			clear(sp.scopeDraw.holds)
		}

		if sp.scopeDraw.allHolds {
			sp.scopeDraw.holds = candidateHolds
			imgui.BeginDisabled()
		}

		const ncol = 4
		if imgui.BeginTableV("holds", ncol, tableFlags, imgui.Vec2{}, 0) {
			if sp.scopeDraw.holds == nil {
				sp.scopeDraw.holds = make(map[string]av.Hold)
			}

			// Display holds
			i := 0
			for name, hold := range util.SortedMap(candidateHolds) {
				if i%ncol == 0 {
					imgui.TableNextRow()
				}
				imgui.TableNextColumn()

				_, enabled := sp.scopeDraw.holds[name]
				enabled = enabled || sp.scopeDraw.allHolds
				if imgui.Checkbox(name+"##hold", &enabled) {
					if enabled {
						sp.scopeDraw.holds[name] = hold
					} else {
						delete(sp.scopeDraw.holds, name)
					}
				}
				i++
			}

			imgui.EndTable()
		}

		if sp.scopeDraw.allHolds {
			imgui.EndDisabled()
		}

	}
}

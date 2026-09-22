// cmd/vice/scenarioinfo.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/gui"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

var acknowledgedATIS = make(map[av.ICAOAirportCode]string)

func drawScenarioInfoWindow(mgr *client.ConnectionManager, config *Config, c *client.ControlClient, activeRadarScope scope.Scope, p platform.Platform, lg *log.Logger) bool {
	// Ensure that the window is wide enough to show the description
	sz := imgui.CalcTextSize(c.State.SimDescription)
	imgui.SetNextWindowSizeConstraints(imgui.Vec2{sz.X + 50, 0}, imgui.Vec2{100000, 100000})

	show := true
	applyPinWindowClass("ScenarioInfo", config, p)
	imgui.BeginV(c.State.SimDescription+"###ScenarioInfo", &show, imgui.WindowFlagsAlwaysAutoResize)
	drawPinButton("ScenarioInfo", config.UnpinnedWindows, p)

	if imgui.CollapsingHeaderBoolPtr("Controllers", nil) {
		// Make big(ish) tables somewhat more legible
		tableFlags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH |
			imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
		if imgui.BeginTableV("controllers", 4, tableFlags, imgui.Vec2{}, 0) {
			imgui.TableSetupColumn("Workstation")
			imgui.TableSetupColumn("Name")
			imgui.TableSetupColumn("Human")
			imgui.TableSetupColumn("Positions")
			imgui.TableHeadersRow()

			// First the potentially-human-controlled ones
			tcws := util.SortedMapKeys(c.State.CurrentConsolidation)
			coveredPositions := make(map[av.ControlPosition]struct{})
			for _, tcw := range tcws {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(controllerDisplayLabel(c.State.Controllers, av.ControlPosition(tcw)))

				imgui.TableNextColumn()
				imgui.Text(c.State.Controllers[av.ControlPosition(tcw)].Callsign)

				imgui.TableNextColumn()
				sq := gui.Icons.CheckSquare
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
						p = append(p, fmt.Sprintf("%s (%s, %s)",
							controllerDisplayLabel(c.State.Controllers, ctrl.PositionId()),
							ctrl.Position,
							ctrl.Frequency.String(),
						))
					}

					var s strings.Builder
					for len(p) > 3 {
						s.WriteString(strings.Join(p[:3], ", ") + "\n")
						p = p[3:]
					}
					s.WriteString(strings.Join(p, ", "))
					imgui.Text(s.String())
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
				imgui.Text(controllerDisplayLabel(c.State.Controllers, ctrl.PositionId()))
				imgui.TableNextColumn()
				imgui.Text(ctrl.Callsign)
				imgui.TableNextColumn()
				imgui.TableNextColumn()
				imgui.Text(fmt.Sprintf("%s (%s, %s)",
					controllerDisplayLabel(c.State.Controllers, ctrl.PositionId()),
					ctrl.Position,
					ctrl.Frequency.String(),
				))
			}

			imgui.EndTable()
		}
	}

	drawScenarioBriefSection(mgr, config, c, p, lg)

	if len(c.State.METAR) > 0 {
		// Collect IFR airports: those with IFR departures or arrivals
		ifrAirports := make(map[av.ICAOAirportCode]bool)
		for ap := range c.State.LaunchConfig.DepartureRates {
			ifrAirports[ap] = true
		}
		for ap := range c.State.ArrivalAirports {
			ifrAirports[ap] = true
		}

		atisExpanded := imgui.CollapsingHeaderBoolPtr("ATIS / METAR", nil)
		if atisExpanded {
			tableFlags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH |
				imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
			if imgui.BeginTableV("atis_metar", 2, tableFlags, imgui.Vec2{}, 0) {
				imgui.TableSetupColumnV("ATIS", imgui.TableColumnFlagsWidthFixed, 0, 0)
				imgui.TableSetupColumn("METAR")
				imgui.TableHeadersRow()

				airports := util.SortedMapKeys(c.State.METAR)
				for _, ap := range airports {
					if !ifrAirports[ap] {
						continue
					}
					letter := c.State.ATISLetter[ap]
					metar := c.State.METAR[ap]

					imgui.TableNextRow()
					imgui.TableNextColumn()

					// Flash if ATIS letter changed since last acknowledgement
					if _, ok := acknowledgedATIS[ap]; !ok {
						acknowledgedATIS[ap] = letter
					}
					gui.PushFont(ui.fixedFont)
					flashing := acknowledgedATIS[ap] != letter
					if flashing && int64(imgui.Time()*2)%2 == 0 {
						imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .2, .2, 1})
					}
					// Center the letter in the column
					colW := imgui.ColumnWidth()
					textW := imgui.CalcTextSize(letter).X
					pad := (colW - textW) / 2
					if pad > 0 {
						imgui.SetCursorPosX(imgui.CursorPosX() + pad)
					}
					if imgui.SelectableBoolV(letter+"##atis_"+string(ap), false, 0, imgui.Vec2{}) {
						acknowledgedATIS[ap] = letter
					}
					if flashing && int64(imgui.Time()*2)%2 == 0 {
						imgui.PopStyleColor()
					}

					imgui.TableNextColumn()
					raw := strings.TrimPrefix(metar.Observation(), "METAR ")
					raw = strings.TrimPrefix(raw, "SPECI ")
					imgui.Text(raw)
					gui.PopFont()
				}

				imgui.EndTable()
			}

		}
	}

	if len(c.State.TFRs) > 0 {
		if imgui.CollapsingHeaderBoolPtr("TFRs", nil) {
			tableFlags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH |
				imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
			if imgui.BeginTableV("tfrs", 5, tableFlags, imgui.Vec2{}, 0) {
				imgui.TableSetupColumn("Type")
				imgui.TableSetupColumn("Name")
				imgui.TableSetupColumn("ARTCC")
				imgui.TableSetupColumn("Effective")
				imgui.TableSetupColumn("Expires")
				imgui.TableHeadersRow()

				gui.PushFont(ui.fixedFont)
				for _, tfr := range c.State.TFRs {
					imgui.TableNextRow()
					imgui.TableNextColumn()
					imgui.Text(tfr.Type)
					rowHovered := imgui.IsItemHovered()
					imgui.TableNextColumn()
					imgui.Text(tfr.LocalName)
					rowHovered = rowHovered || imgui.IsItemHovered()
					imgui.TableNextColumn()
					imgui.Text(tfr.ARTCC)
					rowHovered = rowHovered || imgui.IsItemHovered()
					imgui.TableNextColumn()
					imgui.Text(tfr.Effective.Format("2006-01-02 15:04Z"))
					rowHovered = rowHovered || imgui.IsItemHovered()
					imgui.TableNextColumn()
					imgui.Text(tfr.Expire.Format("2006-01-02 15:04Z"))
					rowHovered = rowHovered || imgui.IsItemHovered()

					if rowHovered {
						var lines []string
						if tfr.Regulation != "" {
							r := tfr.Regulation
							if tfr.Type != "" {
								r += " - " + tfr.Type
							}
							lines = append(lines, r)
						}
						if tfr.City != "" || tfr.State != "" {
							loc := tfr.City
							if loc != "" && tfr.State != "" {
								loc += ", "
							}
							loc += tfr.State
							lines = append(lines, loc)
						}
						if tfr.AltDescr != "" {
							lines = append(lines, tfr.AltDescr)
						}
						if tfr.Purpose != "" {
							lines = append(lines, tfr.Purpose)
						}
						if len(lines) > 0 {
							imgui.SetTooltip(strings.Join(lines, "\n"))
						}
					}
				}
				gui.PopFont()

				imgui.EndTable()
			}
		}
	}

	if draw, ok := activeRadarScope.(scope.InfoWindowDrawer); ok {
		draw.DrawInfo(c, p, lg)
	}
	imgui.End()

	return show
}

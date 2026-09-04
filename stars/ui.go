// ui.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
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

	imgui.Text("Monitor: ")
	for m, colors := range util.SortedMap(monitorColorSets) {
		imgui.SameLine()
		if imgui.RadioButtonBool(m, sp.Monitor == m) {
			sp.Monitor = m
			sp.Colors = colors
		}
	}

	imgui.Checkbox("Lock display", &sp.LockDisplay)

	imgui.Checkbox("Scale DCB to fit", &sp.DCBScaleToFit)

	imgui.Checkbox("Invert numeric keypad", &sp.FlipNumericKeypad)

	imgui.Checkbox("Display error when owned aircraft are outside of your airspace",
		&sp.DisplayOutsideAirspaceWarning)

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
	sp.scopeDraw.DrawArrivalsUI(c, sp.IFPHelpers.ArrivalsColor)
	sp.scopeDraw.DrawApproachesUI(c, sp.IFPHelpers.ApproachesColor, lg)
	sp.scopeDraw.DrawDeparturesUI(c, sp.IFPHelpers.DeparturesColor)
	sp.scopeDraw.DrawOverflightsUI(c, sp.IFPHelpers.OverflightsColor)
	sp.scopeDraw.DrawAirspaceUI(c, sp.IFPHelpers.AirspaceColor)
	radar.DrawTowerListsUI(c)
	radar.DrawAirspaceAwarenessUI(c)

	if macros := c.State.FacilityAdaptation.STARSMacros; len(macros) > 0 {
		if imgui.CollapsingHeaderBoolPtr("STARS Macros", nil) {
			// Sort by mode for grouped display.
			sorted := make(sim.STARSMacroSet, len(macros))
			copy(sorted, macros)
			slices.SortStableFunc(sorted, func(a, b sim.STARSMacro) int {
				return strings.Compare(a.Input, b.Input)
			})

			fixedFont := renderer.GetFont(renderer.FontIdentifier{Name: renderer.RobotoMono, Size: renderer.FixedFontSize(int(imgui.FontSize()))})
			if imgui.BeginTableV("macros", 5, radar.TableFlags, imgui.Vec2{}, 0) {
				imgui.TableSetupColumn("Mode")
				imgui.TableSetupColumn("Input")
				imgui.TableSetupColumn("Activation")
				imgui.TableSetupColumn("Commands")
				imgui.TableSetupColumn("Description")
				imgui.TableHeadersRow()

				for _, m := range sorted {
					imgui.TableNextRow()
					imgui.TableNextColumn()
					fixedFont.ImguiPush()
					if mode := m.Mode(); mode == "" {
						imgui.Text("--")
					} else {
						imgui.Text(mode)
					}
					imgui.PopFont()
					imgui.TableNextColumn()
					fixedFont.ImguiPush()
					if input := m.Name(); input == "" {
						imgui.Text("--")
					} else {
						imgui.Text(input)
					}
					imgui.PopFont()
					imgui.TableNextColumn()
					if m.IsSlew() {
						imgui.Text("Slew")
					} else {
						imgui.Text("Enter")
					}
					imgui.TableNextColumn()
					fixedFont.ImguiPush()
					imgui.Text(strings.Join(m.Commands, "\n"))
					imgui.PopFont()
					imgui.TableNextColumn()
					imgui.Text(m.Description)
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
		if imgui.BeginTableV("holds", ncol, radar.TableFlags, imgui.Vec2{}, 0) {
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

// stars/ui.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"slices"
	"strings"

	"github.com/mmp/vice/client"
	"github.com/mmp/vice/gui"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

var _ scope.UIDrawer = (*Scope)(nil)

func (sp *Scope) DisplayName() string { return "STARS" }

func (sp *Scope) DrawUI(p platform.Platform, config *platform.Config) {
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

	if sp.prefSet != nil { // Hacky workaround to crash if DrawUI runs with no active STARS Scope.
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

func (sp *Scope) DrawInfo(c *client.ControlClient, p platform.Platform, lg *log.Logger) {
	sp.ScenarioRoutes.DrawUI(c, lg)
	scope.DrawTowerListsUI(c)
	scope.DrawAirspaceAwarenessUI(c)

	if macros := c.State.FacilityAdaptation.STARSMacrosForConfiguration(c.State.ConfigurationId); len(macros) > 0 {
		if imgui.CollapsingHeaderBoolPtr("STARS Macros", nil) {
			// Sort by mode for grouped display.
			sorted := make(sim.STARSMacroSet, len(macros))
			copy(sorted, macros)
			slices.SortStableFunc(sorted, func(a, b sim.STARSMacro) int {
				return strings.Compare(a.Input, b.Input)
			})

			fixedFont := gui.GetFont(gui.Fonts.RobotoMono, gui.FixedFontSize(int(imgui.FontSize())))
			if imgui.BeginTableV("macros", 5, scope.TableFlags, imgui.Vec2{}, 0) {
				imgui.TableSetupColumn("Mode")
				imgui.TableSetupColumn("Input")
				imgui.TableSetupColumn("Activation")
				imgui.TableSetupColumn("Commands")
				imgui.TableSetupColumn("Description")
				imgui.TableHeadersRow()

				for _, m := range sorted {
					imgui.TableNextRow()
					imgui.TableNextColumn()
					gui.PushFont(fixedFont)
					if mode := m.Mode(); mode == "" {
						imgui.Text("--")
					} else {
						imgui.Text(mode)
					}
					gui.PopFont()
					imgui.TableNextColumn()
					gui.PushFont(fixedFont)
					if input := m.Name(); input == "" {
						imgui.Text("--")
					} else {
						imgui.Text(input)
					}
					gui.PopFont()
					imgui.TableNextColumn()
					if m.IsSlew() {
						imgui.Text("Slew")
					} else {
						imgui.Text("Enter")
					}
					imgui.TableNextColumn()
					gui.PushFont(fixedFont)
					imgui.Text(strings.Join(m.Commands, "\n"))
					gui.PopFont()
					imgui.TableNextColumn()
					imgui.Text(m.Description)
				}
				imgui.EndTable()
			}
		}
	}

	ps := sp.currentPrefs()
	sp.ScenarioRoutes.DrawHoldsUI(util.Select(ps.UseUserCenter, ps.UserCenter, ps.DefaultCenter), ps.Range)
}

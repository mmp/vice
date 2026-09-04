package eram

import (
	"fmt"
	"maps"
	"strings"

	"github.com/AllenDang/cimgui-go/imgui"
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/radar"
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

	if false {
		drawSymbolGlyphDebugUI(tableFlags)
		drawDashPatternDebugUI(tableFlags)
	}

	if imgui.CollapsingHeaderBoolPtr("Preferences", nil) {
		if imgui.BeginTableV("Saved Preferences", 4, radar.TableFlags, imgui.Vec2{}, 0) {
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
						cp.VideoMapVisible = maps.Clone(cp.VideoMapVisible)
						cp.VideoMapBrightness = maps.Clone(cp.VideoMapBrightness)
						ep.prefSet.Saved[i] = &cp
						ep.tempSavedNames[i] = ""
					}
				}
				imgui.TableNextColumn()
				if imgui.Button("Load") {
					if pref != nil && pref.ARTCC == currentARTCC && pref.VideoMapGroup == currentGroup {
						ep.prefSet.Current = *pref
						// Clone map fields so editing current doesn't mutate saved copy
						ep.prefSet.Current.VideoMapVisible = maps.Clone(pref.VideoMapVisible)
						ep.prefSet.Current.VideoMapBrightness = maps.Clone(pref.VideoMapBrightness)
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
	ep.scopeDraw.DrawArrivalsUI(c, ep.IFPHelpers.ArrivalsColor)
	ep.scopeDraw.DrawApproachesUI(c, ep.IFPHelpers.ApproachesColor, lg)
	ep.scopeDraw.DrawDeparturesUI(c, ep.IFPHelpers.DeparturesColor)
	ep.scopeDraw.DrawOverflightsUI(c, ep.IFPHelpers.OverflightsColor)
	ep.scopeDraw.DrawAirspaceUI(c, ep.IFPHelpers.AirspaceColor)
	radar.DrawTowerListsUI(c)
	radar.DrawAirspaceAwarenessUI(c)
}

// drawSymbolGlyphDebugUI exposes a +/- table for tweaking which
// EramGeomap glyph (0x00..0x0F) each SymbolStyle uses. Every click of a
// step button prints a paste-ready map literal to stdout.
func drawSymbolGlyphDebugUI(tableFlags imgui.TableFlags) {
	if !imgui.CollapsingHeaderBoolPtr("Symbol Glyphs (debug)", nil) {
		return
	}
	if !imgui.BeginTableV("symbolglyphs", 4, radar.TableFlags, imgui.Vec2{}, 0) {
		return
	}
	defer imgui.EndTable()

	imgui.TableSetupColumn("Annotation")
	imgui.TableSetupColumn("Glyph")
	imgui.TableSetupColumn("Dec")
	imgui.TableSetupColumn("Inc")
	imgui.TableHeadersRow()

	const numEramGeomapGlyphs = 16

	var symbolStyleOrder = []av.SymbolStyle{
		av.SymbolStyleVOR,
		av.SymbolStyleNDB,
		av.SymbolStyleTACAN,
		av.SymbolStyleVOR_TACAN,
		av.SymbolStyleDME,
		av.SymbolStyleRNAV,
		av.SymbolStyleRNAVOnlyWaypoint,
		av.SymbolStyleAirport,
		av.SymbolStyleSatelliteAirport,
		av.SymbolStyleEmergencyAirport,
		av.SymbolStyleHeliport,
		av.SymbolStyleOtherWaypoints,
		av.SymbolStyleAirwayIntersections,
		av.SymbolStyleIAF,
		av.SymbolStyleObstruction1,
		av.SymbolStyleObstruction2,
		av.SymbolStyleNuclear,
		av.SymbolStyleRadar,
	}

	changed := false
	for i, style := range symbolStyleOrder {
		imgui.PushIDInt(int32(i))

		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text(style.String())

		imgui.TableNextColumn()
		imgui.Text(fmt.Sprintf("0x%02X", int(symbolGlyphIndex[style])))

		imgui.TableNextColumn()
		if imgui.Button("-##dec") {
			idx := (int(symbolGlyphIndex[style]) - 1 + numEramGeomapGlyphs) % numEramGeomapGlyphs
			symbolGlyphIndex[style] = rune(idx)
			changed = true
		}

		imgui.TableNextColumn()
		if imgui.Button("+##inc") {
			idx := (int(symbolGlyphIndex[style]) + 1) % numEramGeomapGlyphs
			symbolGlyphIndex[style] = rune(idx)
			changed = true
		}

		imgui.PopID()
	}

	if changed {
		var b strings.Builder
		b.WriteString("var symbolGlyphIndex = map[av.SymbolStyle]rune{\n")
		for _, style := range symbolStyleOrder {
			fmt.Fprintf(&b, "\tav.SymbolStyle%-20s 0x%02X,\n",
				style.String()+":", int(symbolGlyphIndex[style]))
		}
		b.WriteString("}\n")
		fmt.Println(b.String())
	}
}

// drawDashPatternDebugUI exposes +/- buttons for tweaking each element of
// the three non-solid dash patterns in window-space pixels.
func drawDashPatternDebugUI(tableFlags imgui.TableFlags) {
	if !imgui.CollapsingHeaderBoolPtr("Dash Patterns (debug)", nil) {
		return
	}

	// Each entry is one editable slot in one of the three slices.
	type slot struct {
		styleName string
		role      string // "dash" / "gap" / "longDash" / "shortDash"
		slice     *[]float32
		idx       int
	}
	slots := []slot{
		{"ShortDashed", "dash", &shortDashedPattern, 0},
		{"ShortDashed", "gap", &shortDashedPattern, 1},
		{"LongDashed", "dash", &longDashedPattern, 0},
		{"LongDashed", "gap", &longDashedPattern, 1},
		{"LongDashShortDash", "longDash", &longDashShortDashPattern, 0},
		{"LongDashShortDash", "gap1", &longDashShortDashPattern, 1},
		{"LongDashShortDash", "shortDash", &longDashShortDashPattern, 2},
		{"LongDashShortDash", "gap2", &longDashShortDashPattern, 3},
	}

	if !imgui.BeginTableV("dashpatterns", 5, radar.TableFlags, imgui.Vec2{}, 0) {
		return
	}
	imgui.TableSetupColumn("Style")
	imgui.TableSetupColumn("Slot")
	imgui.TableSetupColumn("Px")
	imgui.TableSetupColumn("Dec")
	imgui.TableSetupColumn("Inc")
	imgui.TableHeadersRow()

	changed := false
	for i, s := range slots {
		imgui.PushIDInt(int32(i))
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text(s.styleName)
		imgui.TableNextColumn()
		imgui.Text(s.role)
		imgui.TableNextColumn()
		imgui.Text(fmt.Sprintf("%.0f", (*s.slice)[s.idx]))
		imgui.TableNextColumn()
		if imgui.Button("-##dec") {
			if (*s.slice)[s.idx] > 1 {
				(*s.slice)[s.idx]--
				changed = true
			}
		}
		imgui.TableNextColumn()
		if imgui.Button("+##inc") {
			(*s.slice)[s.idx]++
			changed = true
		}
		imgui.PopID()
	}
	imgui.EndTable()

	if changed {
		fmt.Printf("shortDashedPattern       = %#v\n", shortDashedPattern)
		fmt.Printf("longDashedPattern        = %#v\n", longDashedPattern)
		fmt.Printf("longDashShortDashPattern = %#v\n\n", longDashShortDashPattern)
	}
}

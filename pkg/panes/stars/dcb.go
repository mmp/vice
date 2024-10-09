// pkg/panes/stars/dcb.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/util"

	"github.com/brunoga/deep"
)

var (
	dcbButtonColor         = renderer.RGB{0, .173, 0}
	dcbActiveButtonColor   = renderer.RGB{0, .305, 0}
	dcbTextColor           = renderer.RGB{1, 1, 1}
	dcbTextSelectedColor   = renderer.RGB{1, 1, 0}
	dcbDisabledButtonColor = renderer.RGB{.4, .4, .4}
	dcbDisabledTextColor   = renderer.RGB{.8, .8, .8}
)

const dcbButtonSize = 84
const numDCBSlots = 20

const (
	buttonFull = 1 << iota
	buttonHalfVertical
	buttonHalfHorizontal
	buttonSelected
	buttonWXAVL
)

const (
	dcbMenuMain = iota
	dcbMenuAux
	dcbMenuMaps
	dcbMenuBrite
	dcbMenuCharSize
	dcbMenuPref
	dcbMenuSite
	dcbMenuSSAFilter
	dcbMenuGITextFilter
	dcbMenuTPA
)

const (
	dcbPositionTop = iota
	dcbPositionLeft
	dcbPositionRight
	dcbPositionBottom
)

// dcbSpinner is an interface used to manage the various spinner types in
// the DCB menu. Since the various spinners generally have unique
// requirements and expectations about keyboard input, having this
// interface allows collecting all of that in the various implementations
// of the interface.
type dcbSpinner interface {
	// Label returns the text that should be shown in the DCB button.
	Label() string

	// Equal returns true if the provided spinner controls the same value
	// as this spinner.
	Equals(other dcbSpinner) bool

	// MouseWheel is called when the spinner is active and there is mouse
	// wheel input; implementations should update the underlying value
	// accordingly.
	MouseWheel(delta int)

	// KeyboardInput is called if the spinner is active and the user enters
	// text and presses enter; implementations should update the underlying
	// value accordingly.
	KeyboardInput(text string) error

	// Disabled is called after a spinner has been disabled, e.g. due to a
	// second click on its DCB button or pressing enter.
	Disabled()
}

func (sp *STARSPane) disableMenuSpinner(ctx *panes.Context) {
	if activeSpinner != nil {
		activeSpinner.Disabled()
	}
	activeSpinner = nil
	ctx.Platform.EndCaptureMouse()
	sp.commandMode = CommandModeNone
}

func (sp *STARSPane) activateMenuSpinner(spinner dcbSpinner) {
	activeSpinner = spinner
}

func (sp *STARSPane) dcbButtonScale(ctx *panes.Context) float32 {
	ps := sp.currentPrefs()
	// Sigh; on windows we want the button size in pixels on high DPI displays
	ds := ctx.DrawPixelScale
	// Scale based on width or height available depending on DCB position
	if ps.DCBPosition == dcbPositionTop || ps.DCBPosition == dcbPositionBottom {
		return math.Min(ds, (ds*ctx.PaneExtent.Width()-4)/(numDCBSlots*dcbButtonSize))
	} else {
		return math.Min(ds, (ds*ctx.PaneExtent.Height()-4)/(numDCBSlots*dcbButtonSize))
	}
}

func (sp *STARSPane) drawDCB(ctx *panes.Context, transforms ScopeTransformations, cb *renderer.CommandBuffer) math.Extent2D {
	ps := sp.currentPrefs()

	// Find a scale factor so that the buttons all fit in the window, if necessary
	buttonScale := sp.dcbButtonScale(ctx)

	sp.startDrawDCB(ctx, buttonScale, transforms, cb)

	drawVideoMapButton := func(idx int) {
		if idx < len(sp.dcbVideoMaps) && sp.dcbVideoMaps[idx] != nil && sp.dcbVideoMaps[idx].Id != 0 {
			m := sp.dcbVideoMaps[idx]

			// Get the map label, either the default or user-specified.
			label := m.Label
			if l, ok := ctx.ControlClient.STARSFacilityAdaptation.VideoMapLabels[m.Name]; ok {
				label = l
			}

			text := fmt.Sprintf("%d\n%s", m.Id, label)
			_, vis := ps.VideoMapVisible[m.Id]
			if toggleButton(ctx, text, &vis, buttonHalfVertical, buttonScale) {
				if vis {
					ps.VideoMapVisible[m.Id] = nil
				} else {
					delete(ps.VideoMapVisible, m.Id)
				}
			}
		} else {
			// Inert button
			off := false
			toggleButton(ctx, "", &off, buttonHalfVertical, buttonScale)
		}
	}

	switch sp.activeDCBMenu {
	case dcbMenuMain:
		sp.drawDCBSpinner(ctx, makeRadarRangeSpinner(&ps.Range), CommandModeRange,
			buttonFull, buttonScale)
		sp.placeButton(ctx, "PLACE\nCNTR", buttonHalfVertical, buttonScale,
			func(pw [2]float32, transforms ScopeTransformations) (status CommandStatus) {
				ps.Center = transforms.LatLongFromWindowP(pw)
				ps.CurrentCenter = ps.Center
				status.clear = true
				return
			})
		offcenter := ps.CurrentCenter != ps.Center
		if toggleButton(ctx, "OFF\nCNTR", &offcenter, buttonHalfVertical, buttonScale) {
			ps.CurrentCenter = ps.Center
		}
		sp.drawDCBSpinner(ctx, makeRangeRingRadiusSpinner(&ps.RangeRingRadius), CommandModeRangeRings,
			buttonFull, buttonScale)
		sp.placeButton(ctx, "PLACE\nRR", buttonHalfVertical, buttonScale,
			func(pw [2]float32, transforms ScopeTransformations) (status CommandStatus) {
				ps.RangeRingsCenter = transforms.LatLongFromWindowP(pw)
				ps.RangeRingsUserCenter = true
				status.clear = true
				return
			})
		defaultCenter := !ps.RangeRingsUserCenter
		if toggleButton(ctx, "RR\nCNTR", &defaultCenter, buttonHalfVertical, buttonScale) {
			ps.RangeRingsUserCenter = !defaultCenter
		}
		if selectButton(ctx, "MAPS", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuMaps
		}
		for i := 0; i < 6; i++ {
			// Maps are given left->right, top->down, but we draw the
			// buttons top->down, left->right, so the indexing is a little
			// funny.
			idx := util.Select(i&1 == 0, i/2, 3+i/2)
			drawVideoMapButton(idx)
		}
		haveWeather := sp.weatherRadar.HaveWeather()
		for i := range ps.DisplayWeatherLevel {
			label := "WX" + strconv.Itoa(i+1)
			flags := buttonHalfHorizontal
			if haveWeather[i] {
				label += "\nAVL"
				flags = flags | buttonWXAVL
			}
			toggleButton(ctx, label, &ps.DisplayWeatherLevel[i], flags, buttonScale)
		}
		if selectButton(ctx, "BRITE", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuBrite
		}
		sp.drawDCBSpinner(ctx, makeLeaderLineDirectionSpinner(sp, &ps.LeaderLineDirection), CommandModeNone,
			buttonHalfVertical, buttonScale)
		sp.drawDCBSpinner(ctx, makeLeaderLineLengthSpinner(&ps.LeaderLineLength), CommandModeLDR,
			buttonHalfVertical, buttonScale)

		if selectButton(ctx, "CHAR\nSIZE", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuCharSize
		}
		disabledButton(ctx, "MODE\nFSL", buttonFull, buttonScale)

		site := sp.radarSiteId(ctx.ControlClient.RadarSites)
		if len(ctx.ControlClient.RadarSites) == 0 {
			disabledButton(ctx, "SITE\n"+site, buttonFull, buttonScale)
		} else {
			if selectButton(ctx, "SITE\n"+site, buttonFull, buttonScale) {
				sp.activeDCBMenu = dcbMenuSite
			}
		}

		pref := "PREF"
		if sp.prefSet.Selected != nil && sp.prefSet.Saved[*sp.prefSet.Selected] != nil {
			pref += "\n" + sp.prefSet.Saved[*sp.prefSet.Selected].Name
		}
		if selectButton(ctx, pref, buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuPref
			// Don't alias anything in the restore values
			sp.RestorePreferences = sp.prefSet.Current.Duplicate()
			if sp.prefSet.Selected == nil {
				sp.RestorePreferencesNumber = nil
			} else {
				sel := *sp.prefSet.Selected
				sp.RestorePreferencesNumber = &sel
			}
		}

		if selectButton(ctx, "SSA\nFILTER", buttonHalfVertical, buttonScale) {
			sp.activeDCBMenu = dcbMenuSSAFilter
		}
		if selectButton(ctx, "GI TEXT\nFILTER", buttonHalfVertical, buttonScale) {
			sp.activeDCBMenu = dcbMenuGITextFilter
		}
		if selectButton(ctx, "SHIFT", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuAux
		}

	case dcbMenuAux:
		sp.drawDCBSpinner(ctx, makeAudioVolumeSpinner(ctx.Platform, sp, &ps.AudioVolume),
			CommandModeNone, buttonFull, buttonScale)
		sp.drawDCBSpinner(ctx, makeIntegerRangeSpinner("HISTORY\n", &ps.RadarTrackHistory, 0, 10),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.drawDCBSpinner(ctx, makeHistoryRateSpinner(&ps.RadarTrackHistoryRate),
			CommandModeNone, buttonHalfVertical, buttonScale)
		disabledButton(ctx, "CURSOR\nHOME", buttonFull, buttonScale)
		disabledButton(ctx, "CSR SPD\n4", buttonFull, buttonScale)
		disabledButton(ctx, "MAP\nUNCOR", buttonFull, buttonScale)
		disabledButton(ctx, "UNCOR", buttonFull, buttonScale)
		disabledButton(ctx, "BEACON\nMODE-2", buttonFull, buttonScale)
		disabledButton(ctx, "RTQC", buttonFull, buttonScale)
		disabledButton(ctx, "MCP", buttonFull, buttonScale)
		top := ps.DCBPosition == dcbPositionTop
		if toggleButton(ctx, "DCB\nTOP", &top, buttonHalfVertical, buttonScale) {
			ps.DCBPosition = dcbPositionTop
		}
		left := ps.DCBPosition == dcbPositionLeft
		if toggleButton(ctx, "DCB\nLEFT", &left, buttonHalfVertical, buttonScale) {
			ps.DCBPosition = dcbPositionLeft
		}
		right := ps.DCBPosition == dcbPositionRight
		if toggleButton(ctx, "DCB\nRIGHT", &right, buttonHalfVertical, buttonScale) {
			ps.DCBPosition = dcbPositionRight
		}
		bottom := ps.DCBPosition == dcbPositionBottom
		if toggleButton(ctx, "DCB\nBOTTOM", &bottom, buttonHalfVertical, buttonScale) {
			ps.DCBPosition = dcbPositionBottom
		}
		sp.drawDCBSpinner(ctx, makePTLLengthSpinner(&ps.PTLLength), CommandModeNone, buttonFull, buttonScale)
		if ps.PTLLength > 0 {
			if toggleButton(ctx, "PTL OWN", &ps.PTLOwn, buttonHalfVertical, buttonScale) && ps.PTLOwn {
				ps.PTLAll = false
			}
			if toggleButton(ctx, "PTL ALL", &ps.PTLAll, buttonHalfVertical, buttonScale) && ps.PTLAll {
				ps.PTLOwn = false
			}
		} else {
			disabledButton(ctx, "PTL OWN", buttonHalfVertical, buttonScale)
			disabledButton(ctx, "PTL ALL", buttonHalfVertical, buttonScale)

		}
		sp.drawDCBSpinner(ctx, makeDwellModeSpinner(&ps.DwellMode), CommandModeNone, buttonFull, buttonScale)
		if selectButton(ctx, "TPA/\nATPA", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuTPA
		}
		if selectButton(ctx, "SHIFT", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuMain
		}

	case dcbMenuMaps:
		if selectButton(ctx, "DONE", buttonHalfVertical, buttonScale) {
			sp.activeDCBMenu = dcbMenuMain
		}
		if selectButton(ctx, "CLR ALL", buttonHalfVertical, buttonScale) {
			clear(ps.VideoMapVisible)
		}

		// Figure out how many map category buttons we need
		var haveCategory [VideoMapNumCategories]bool
		for _, vm := range sp.allVideoMaps {
			if vm.Category != VideoMapNoCategory {
				haveCategory[vm.Category] = true
			}
		}
		ncat := 0
		for _, b := range haveCategory {
			if b {
				ncat++
			}
		}

		// On the right side, we need at least one column for CURRENT and
		// then there's one slot above that; we then take as many full
		// columns as necessary for the categories we have.
		catCols := 1 + (ncat)/2

		// Draw buttons with the space left.
		for i := range 32 - 2*catCols {
			// Indexing is tricky both because we are skipping the first 6
			// maps, which are shown in the main DCB, but also because we
			// draw top->down, left->right while the maps are specified
			// left->right, top->down...
			idx := util.Select(i&1 == 0, 6+i/2, 22+i/2)
			drawVideoMapButton(idx)
		}

		mapLabels := [VideoMapNumCategories]string{
			VideoMapGeographicMaps:     "GEO\nMAPS",
			VideoMapControlledAirspace: "CONTROL",
			VideoMapRunwayExtensions:   "RUNWAYS",
			VideoMapDangerAreas:        "DANGER\nAREAS",
			VideoMapAerodromes:         "AIRPORT",
			VideoMapGeneralAviation:    "GENERAL\nAV",
			VideoMapSIDsSTARs:          "SID\nSTAR",
			VideoMapMilitary:           "MIL",
			VideoMapGeographicPoints:   "GEO\nPOINTS",
			VideoMapProcessingAreas:    "SYS\nPROC",
		}
		for cat, b := range haveCategory {
			if b {
				sel := int(ps.VideoMapsList.Selection) == cat && ps.VideoMapsList.Visible
				if toggleButton(ctx, mapLabels[cat], &sel, buttonHalfVertical, buttonScale) {
					ps.VideoMapsList.Selection = VideoMapsGroup(cat)
					ps.VideoMapsList.Visible = sel
				}
			}
		}
		if ncat&1 == 0 {
			// If there are an even number then there's an extra empty button above CURRENT
			off := false
			toggleButton(ctx, "", &off, buttonHalfVertical, buttonScale)
		}

		currentMapsSelected := ps.VideoMapsList.Selection == VideoMapCurrent && ps.VideoMapsList.Visible
		if toggleButton(ctx, "CURRENT", &currentMapsSelected, buttonHalfVertical, buttonScale) {
			ps.VideoMapsList.Selection = VideoMapCurrent
			ps.VideoMapsList.Visible = currentMapsSelected
		}

	case dcbMenuBrite:
		sp.drawDCBSpinner(ctx, makeBrightnessSpinner("DCB", &ps.Brightness.DCB, 25, false),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.drawDCBSpinner(ctx, makeBrightnessSpinner("BKC", &ps.Brightness.BackgroundContrast, 0, false),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.drawDCBSpinner(ctx, makeBrightnessSpinner("MPA", &ps.Brightness.VideoGroupA, 5, false),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.drawDCBSpinner(ctx, makeBrightnessSpinner("MPB", &ps.Brightness.VideoGroupB, 5, false),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.drawDCBSpinner(ctx, makeBrightnessSpinner("FDB", &ps.Brightness.FullDatablocks, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.drawDCBSpinner(ctx, makeBrightnessSpinner("LST", &ps.Brightness.Lists, 25, false),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.drawDCBSpinner(ctx, makeBrightnessSpinner("POS", &ps.Brightness.Positions, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.drawDCBSpinner(ctx, makeBrightnessSpinner("LDB", &ps.Brightness.LimitedDatablocks, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.drawDCBSpinner(ctx, makeBrightnessSpinner("OTH", &ps.Brightness.OtherTracks, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.drawDCBSpinner(ctx, makeBrightnessSpinner("TLS", &ps.Brightness.Lines, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.drawDCBSpinner(ctx, makeBrightnessSpinner("RR", &ps.Brightness.RangeRings, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.drawDCBSpinner(ctx, makeBrightnessSpinner("CMP", &ps.Brightness.Compass, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.drawDCBSpinner(ctx, makeBrightnessSpinner("BCN", &ps.Brightness.BeaconSymbols, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.drawDCBSpinner(ctx, makeBrightnessSpinner("PRI", &ps.Brightness.PrimarySymbols, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.drawDCBSpinner(ctx, makeBrightnessSpinner("HST", &ps.Brightness.History, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.drawDCBSpinner(ctx, makeBrightnessSpinner("WX", &ps.Brightness.Weather, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.drawDCBSpinner(ctx, makeBrightnessSpinner("WXC", &ps.Brightness.WxContrast, 5, false),
			CommandModeNone, buttonHalfVertical, buttonScale)
		if selectButton(ctx, "DONE", buttonHalfVertical, buttonScale) {
			sp.activeDCBMenu = dcbMenuMain
		}

	case dcbMenuCharSize:
		sp.drawDCBSpinner(ctx, makeIntegerRangeSpinner("DATA\nBLOCKS\n", &ps.CharSize.Datablocks, 0, 5),
			CommandModeNone, buttonFull, buttonScale)
		sp.drawDCBSpinner(ctx, makeIntegerRangeSpinner("LISTS\n", &ps.CharSize.Lists, 0, 5),
			CommandModeNone, buttonFull, buttonScale)
		sp.drawDCBSpinner(ctx, makeIntegerRangeSpinner("DCB\n", &ps.CharSize.DCB, 0, 2),
			CommandModeNone, buttonFull, buttonScale)
		sp.drawDCBSpinner(ctx, makeIntegerRangeSpinner("TOOLS\n", &ps.CharSize.Tools, 0, 5),
			CommandModeNone, buttonFull, buttonScale)
		sp.drawDCBSpinner(ctx, makeIntegerRangeSpinner("POS\n", &ps.CharSize.PositionSymbols, 0, 5),
			CommandModeNone, buttonFull, buttonScale)
		if selectButton(ctx, "DONE", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuMain
		}

	case dcbMenuPref:
		for i, prefs := range sp.prefSet.Saved {
			text := strconv.Itoa(i+1) + "\n"
			flags := buttonHalfVertical
			if prefs == nil {
				disabledButton(ctx, text, flags, buttonScale)
			} else {
				text += prefs.Name
				if sp.prefSet.Selected != nil && i == *sp.prefSet.Selected {
					flags = flags | buttonSelected
				}
				if selectButton(ctx, text, flags, buttonScale) {
					// Make this one current
					idx := i // copy since i is a loop iteration variable..
					sp.prefSet.Selected = &idx
					// Load the prefs.
					sp.prefSet.SetCurrent(*sp.prefSet.Saved[i], ctx.Platform, sp)
				}
			}
		}

		if selectButton(ctx, "DEFAULT", buttonHalfVertical, buttonScale) {
			sp.prefSet.ResetDefault(ctx.ControlClient.State, ctx.Platform, sp)
		}
		disabledButton(ctx, "FSSTARS", buttonHalfVertical, buttonScale)
		if sp.RestorePreferences == nil {
			// It shouldn't be nil, but...
			disabledButton(ctx, "RESTORE", buttonHalfVertical, buttonScale)
		} else if selectButton(ctx, "RESTORE", buttonHalfVertical, buttonScale) {
			// 4-20: restore display settings that were in effect when we
			// entered the PREF sub-menu.
			sp.prefSet.Current = deep.MustCopy(*sp.RestorePreferences)
			sp.prefSet.Current.Activate(ctx.Platform, sp)
			if sp.RestorePreferencesNumber == nil {
				sp.prefSet.Selected = nil
			} else {
				n := *sp.RestorePreferencesNumber
				sp.prefSet.Selected = &n
			}
		}

		if sp.prefSet.Selected != nil {
			if selectButton(ctx, "SAVE", buttonHalfVertical, buttonScale) {
				sp.prefSet.Saved[*sp.prefSet.Selected] = sp.prefSet.Current.Duplicate()
			}
		} else {
			disabledButton(ctx, "SAVE", buttonHalfVertical, buttonScale)
		}
		disabledButton(ctx, "CHG PIN", buttonHalfVertical, buttonScale)

		canSaveAs := slices.Contains(sp.prefSet.Saved[:], nil)
		if !canSaveAs {
			disabledButton(ctx, "SAVE AS", buttonHalfVertical, buttonScale)
		} else if selectButton(ctx, "SAVE AS", buttonHalfVertical, buttonScale) {
			// This command mode handles prompting for the name and then
			// saves when enter is pressed.
			sp.commandMode = CommandModeSavePrefAs
		}
		if sp.prefSet.Selected != nil {
			if selectButton(ctx, "DELETE", buttonHalfVertical, buttonScale) {
				sp.prefSet.Saved[*sp.prefSet.Selected] = nil
				sp.prefSet.Selected = nil
			}
		} else {
			disabledButton(ctx, "DELETE", buttonHalfVertical, buttonScale)
		}

		if selectButton(ctx, "DONE", buttonHalfVertical, buttonScale) {
			sp.activeDCBMenu = dcbMenuMain
			sp.RestorePreferences = nil
			sp.RestorePreferencesNumber = nil
		}

	case dcbMenuSite:
		for _, id := range util.SortedMapKeys(ctx.ControlClient.RadarSites) {
			site := ctx.ControlClient.RadarSites[id]
			label := " " + site.Char + " " + "\n" + id
			selected := ps.RadarSiteSelected == id
			if toggleButton(ctx, label, &selected, buttonFull, buttonScale) {
				if selected {
					ps.RadarSiteSelected = id
				} else {
					ps.RadarSiteSelected = ""
				}
			}
		}
		// Fill extras with empty disabled buttons
		for i := len(ctx.ControlClient.RadarSites); i < 15; i++ {
			disabledButton(ctx, "", buttonFull, buttonScale)
		}
		multi := sp.radarMode(ctx.ControlClient.RadarSites) == RadarModeMulti
		if toggleButton(ctx, "MULTI", &multi, buttonFull, buttonScale) && multi {
			ps.RadarSiteSelected = ""
			if ps.FusedRadarMode {
				sp.discardTracks = true
			}
			ps.FusedRadarMode = false
		}
		fused := sp.radarMode(ctx.ControlClient.RadarSites) == RadarModeFused
		if toggleButton(ctx, "FUSED", &fused, buttonFull, buttonScale) && fused {
			ps.RadarSiteSelected = ""
			ps.FusedRadarMode = true
			sp.discardTracks = true
		}
		if selectButton(ctx, "DONE", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuMain
		}

	case dcbMenuSSAFilter:
		// 4-44 / 2-71
		toggleButton(ctx, "ALL", &ps.SSAList.Filter.All, buttonHalfVertical, buttonScale)
		toggleButton(ctx, "WX", &ps.SSAList.Filter.Wx, buttonHalfVertical, buttonScale)
		toggleButton(ctx, "TIME", &ps.SSAList.Filter.Time, buttonHalfVertical, buttonScale)
		toggleButton(ctx, "ALTSTG", &ps.SSAList.Filter.Altimeter, buttonHalfVertical, buttonScale)
		toggleButton(ctx, "STATUS", &ps.SSAList.Filter.Status, buttonHalfVertical, buttonScale)
		disabledButton(ctx, "PLAN", buttonHalfVertical, buttonScale) // ?? TODO
		toggleButton(ctx, "RADAR", &ps.SSAList.Filter.Radar, buttonHalfVertical, buttonScale)
		toggleButton(ctx, "CODES", &ps.SSAList.Filter.Codes, buttonHalfVertical, buttonScale)
		toggleButton(ctx, "SPC", &ps.SSAList.Filter.SpecialPurposeCodes, buttonHalfVertical, buttonScale)
		disabledButton(ctx, "SYS OFF", buttonHalfVertical, buttonScale) // ?? TODO
		toggleButton(ctx, "RANGE", &ps.SSAList.Filter.Range, buttonHalfVertical, buttonScale)
		toggleButton(ctx, "PTL", &ps.SSAList.Filter.PredictedTrackLines, buttonHalfVertical, buttonScale)
		toggleButton(ctx, "ALT FIL", &ps.SSAList.Filter.AltitudeFilters, buttonHalfVertical, buttonScale)
		disabledButton(ctx, "NAS I/F", buttonHalfVertical, buttonScale) // ?? TODO
		// INTRAIL
		// 2.5
		toggleButton(ctx, "AIRPORT", &ps.SSAList.Filter.AirportWeather, buttonHalfVertical, buttonScale)
		disabledButton(ctx, "OP MODE", buttonHalfVertical, buttonScale) // ?? TODO
		disabledButton(ctx, "TT", buttonHalfVertical, buttonScale)      // ?? TODO
		toggleButton(ctx, "WX HIST", &ps.SSAList.Filter.WxHistory, buttonHalfVertical, buttonScale)
		toggleButton(ctx, "QL", &ps.SSAList.Filter.QuickLookPositions, buttonHalfVertical, buttonScale)
		toggleButton(ctx, "TW OFF", &ps.SSAList.Filter.DisabledTerminal, buttonHalfVertical, buttonScale)
		disabledButton(ctx, "CON/CPL", buttonHalfVertical, buttonScale) // ?? TODO
		disabledButton(ctx, "OFF IND", buttonHalfVertical, buttonScale) // ?? TODO
		toggleButton(ctx, "CRDA", &ps.SSAList.Filter.ActiveCRDAPairs, buttonHalfVertical, buttonScale)
		disabledButton(ctx, "FLOW", buttonHalfVertical, buttonScale) // TODO
		disabledButton(ctx, "AMZ", buttonHalfVertical, buttonScale)  // TODO
		disabledButton(ctx, "TBFM", buttonHalfVertical, buttonScale) // TODO
		if selectButton(ctx, "DONE", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuMain
		}

	case dcbMenuGITextFilter:
		toggleButton(ctx, "MAIN", &ps.SSAList.Filter.Text.Main, buttonHalfVertical, buttonScale)
		for i := range ps.SSAList.Filter.Text.GI {
			toggleButton(ctx, fmt.Sprintf("GI %d", i+1), &ps.SSAList.Filter.Text.GI[i],
				buttonHalfVertical, buttonScale)
		}
		if selectButton(ctx, "DONE", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuMain
		}

	case dcbMenuTPA:
		onoff := func(b bool) string { return util.Select(b, "ENABLED", "INHIBTD") }
		if selectButton(ctx, "A/TPA\nMILEAGE\n"+onoff(ps.DisplayTPASize), buttonFull, buttonScale) {
			ps.DisplayTPASize = !ps.DisplayTPASize
		}
		if selectButton(ctx, "INTRAIL\nDIST\n"+onoff(ps.DisplayATPAInTrailDist), buttonFull, buttonScale) {
			ps.DisplayATPAInTrailDist = !ps.DisplayATPAInTrailDist
		}
		if selectButton(ctx, "ALERT\nCONES\n"+onoff(ps.DisplayATPAWarningAlertCones), buttonFull, buttonScale) {
			ps.DisplayATPAWarningAlertCones = !ps.DisplayATPAWarningAlertCones
		}
		if selectButton(ctx, "MONITOR\nCONES\n"+onoff(ps.DisplayATPAMonitorCones), buttonFull, buttonScale) {
			ps.DisplayATPAMonitorCones = !ps.DisplayATPAMonitorCones
		}
		if selectButton(ctx, "DONE", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuAux
		}
	}

	sp.endDrawDCB()

	sz := buttonSize(buttonFull, buttonScale)
	paneExtent := ctx.PaneExtent
	switch ps.DCBPosition {
	case dcbPositionTop:
		paneExtent.P1[1] -= sz[1]

	case dcbPositionLeft:
		paneExtent.P0[0] += sz[0]

	case dcbPositionRight:
		paneExtent.P1[0] -= sz[0]

	case dcbPositionBottom:
		paneExtent.P0[1] += sz[1]
	}

	return paneExtent
}

func buttonSize(flags int, scale float32) [2]float32 {
	bs := func(s float32) float32 { return float32(int(s*dcbButtonSize + 0.5)) }

	if (flags & buttonFull) != 0 {
		return [2]float32{bs(scale), bs(scale)}
	} else if (flags & buttonHalfVertical) != 0 {
		return [2]float32{bs(scale), bs(scale / 2)}
	} else if (flags & buttonHalfHorizontal) != 0 {
		return [2]float32{bs(scale / 2), bs(scale)}
	} else {
		panic(fmt.Sprintf("unhandled starsButtonFlags %d", flags))
	}
}

var dcbDrawState struct {
	cb           *renderer.CommandBuffer
	mouse        *platform.MouseState
	mouseDownPos []float32
	cursor       [2]float32
	drawStartPos [2]float32
	style        renderer.TextStyle
	brightness   STARSBrightness
	position     int
}

func (sp *STARSPane) startDrawDCB(ctx *panes.Context, buttonScale float32, transforms ScopeTransformations,
	cb *renderer.CommandBuffer) {
	dcbDrawState.cb = cb
	dcbDrawState.mouse = ctx.Mouse

	ps := sp.currentPrefs()
	dcbDrawState.brightness = ps.Brightness.DCB
	dcbDrawState.position = ps.DCBPosition
	buttonSize := float32(int(sp.dcbButtonScale(ctx)*dcbButtonSize + 0.5))
	var drawEndPos [2]float32
	switch dcbDrawState.position {
	case dcbPositionTop:
		dcbDrawState.drawStartPos = [2]float32{0, ctx.PaneExtent.Height() - 1}
		drawEndPos = [2]float32{ctx.PaneExtent.Width(), dcbDrawState.drawStartPos[1] - buttonSize}

	case dcbPositionLeft:
		dcbDrawState.drawStartPos = [2]float32{0, ctx.PaneExtent.Height() - 1}
		drawEndPos = [2]float32{buttonSize, 0}

	case dcbPositionRight:
		dcbDrawState.drawStartPos = [2]float32{ctx.PaneExtent.Width() - buttonSize, ctx.PaneExtent.Height()}
		drawEndPos = [2]float32{dcbDrawState.drawStartPos[0] - buttonSize, 0}

	case dcbPositionBottom:
		dcbDrawState.drawStartPos = [2]float32{0, buttonSize}
		drawEndPos = [2]float32{ctx.PaneExtent.Width(), 0}
	}

	dcbDrawState.cursor = dcbDrawState.drawStartPos

	dcbDrawState.style = renderer.TextStyle{
		Font:        sp.dcbFont[ps.CharSize.DCB],
		Color:       renderer.RGB{1, 1, 1},
		LineSpacing: 0,
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)

	// Draw background color quad
	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	trid.AddQuad(dcbDrawState.drawStartPos, [2]float32{drawEndPos[0], dcbDrawState.drawStartPos[1]},
		drawEndPos, [2]float32{dcbDrawState.drawStartPos[0], drawEndPos[1]},
		renderer.RGB{0, 0.05, 0})
	trid.GenerateCommands(cb)

	if ctx.Mouse != nil && ctx.Mouse.Clicked[platform.MouseButtonPrimary] {
		dcbDrawState.mouseDownPos = ctx.Mouse.Pos[:]
	}
}

func (sp *STARSPane) endDrawDCB() {
	// Clear out the scissor et al...
	dcbDrawState.cb.ResetState()

	if mouse := dcbDrawState.mouse; mouse != nil {
		if mouse.Released[platform.MouseButtonPrimary] {
			dcbDrawState.mouseDownPos = nil
		}
	}
}

func drawDCBText(text string, td *renderer.TextDrawBuilder, buttonSize [2]float32, color renderer.RGB) {
	// Clean up the text
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}

	style := dcbDrawState.style
	style.Color = renderer.LerpRGB(.5, color, dcbDrawState.brightness.ScaleRGB(color))
	_, h := style.Font.BoundText(strings.Join(lines, "\n"), dcbDrawState.style.LineSpacing)

	slop := buttonSize[1] - float32(h) // todo: what if negative...
	y0 := dcbDrawState.cursor[1] - 1 - slop/2
	for _, line := range lines {
		lw, lh := style.Font.BoundText(line, style.LineSpacing)
		// Try to center the text, though if it's too big to fit in the
		// button then draw it starting from the left edge of the button so
		// that the trailing characters are the ones that are lost.
		x0 := dcbDrawState.cursor[0] + math.Max(1, (buttonSize[0]-float32(lw))/2)

		td.AddText(line, [2]float32{x0, y0}, style)
		y0 -= float32(lh)
	}
}

func drawDCBButton(ctx *panes.Context, text string, flags int, buttonScale float32, pushedIn bool, disabled bool) (math.Extent2D, bool) {
	ld := renderer.GetColoredLinesDrawBuilder()
	trid := renderer.GetColoredTrianglesDrawBuilder()
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	defer renderer.ReturnTextDrawBuilder(td)

	sz := buttonSize(flags, buttonScale)

	// Offset for spacing
	const delta = 1
	p0 := math.Add2f(dcbDrawState.cursor, [2]float32{delta, -delta})
	p1 := math.Add2f(p0, [2]float32{sz[0] - 2*delta, 0})
	p2 := math.Add2f(p1, [2]float32{0, -sz[1] + 2*delta})
	p3 := math.Add2f(p2, [2]float32{-sz[0] + 2*delta, 0})

	ext := math.Extent2DFromPoints([][2]float32{p0, p2})
	mouse := dcbDrawState.mouse
	mouseInside := mouse != nil && ext.Inside(mouse.Pos)
	mouseDownInside := dcbDrawState.mouseDownPos != nil &&
		ext.Inside([2]float32{dcbDrawState.mouseDownPos[0], dcbDrawState.mouseDownPos[1]})

	var buttonColor, textColor renderer.RGB
	if disabled {
		buttonColor = dcbDisabledButtonColor
		textColor = dcbDisabledTextColor
	}
	if !disabled {
		if mouseInside && mouseDownInside {
			pushedIn = !pushedIn
		}

		// Swap selected/regular color to indicate the tentative result
		if flags&buttonWXAVL != 0 {
			buttonColor = util.Select(pushedIn, renderer.RGBFromUInt8(116, 116, 162), // 70,70,100
				renderer.RGBFromUInt8(83, 83, 162)) // 50,50,100
		} else {
			buttonColor = util.Select(pushedIn, dcbActiveButtonColor, dcbButtonColor)
		}
		textColor = util.Select(mouseInside, dcbTextSelectedColor, dcbTextColor)
	}
	buttonColor = dcbDrawState.brightness.ScaleRGB(buttonColor)
	//textColor = dcbDrawState.brightness.ScaleRGB(textColor)

	trid.AddQuad(p0, p1, p2, p3, buttonColor)
	drawDCBText(text, td, sz, textColor)

	// Draw the bevel around the button
	topLeftBevelColor := renderer.RGB{0.2, 0.2, 0.2}
	bottomRightBevelColor := renderer.RGB{0, 0, 0}
	shiftp := func(p [2]float32, dx, dy float32) [2]float32 {
		return math.Add2f(p, [2]float32{dx, dy})
	}
	if !disabled && pushedIn { //((selected && !mouseInside) || (!selected && mouseInside && mouse.Down[MouseButtonPrimary])) {
		// Depressed bevel scheme: darker top/left, highlight bottom/right
		topLeftBevelColor, bottomRightBevelColor = bottomRightBevelColor, topLeftBevelColor
	}
	// Draw the bevel via individual 1-pixel lines (note that down is negative y...)
	// Top, with the right end pulled left
	ld.AddLine(p0, p1, topLeftBevelColor)
	ld.AddLine(shiftp(p0, 0, -1), shiftp(p1, -1, -1), topLeftBevelColor)
	ld.AddLine(shiftp(p0, 0, -2), shiftp(p1, -2, -2), topLeftBevelColor)
	// Left side with bottom end pulled up
	ld.AddLine(p0, p3, topLeftBevelColor)
	ld.AddLine(shiftp(p0, 1, 0), shiftp(p3, 1, 1), topLeftBevelColor)
	ld.AddLine(shiftp(p0, 2, 0), shiftp(p3, 2, 2), topLeftBevelColor)
	// Right side with top pulled down
	ld.AddLine(p1, p2, bottomRightBevelColor)
	ld.AddLine(shiftp(p1, -1, -1), shiftp(p2, -1, 0), bottomRightBevelColor)
	ld.AddLine(shiftp(p1, -2, -2), shiftp(p2, -2, 0), bottomRightBevelColor)
	// Bottom with left end pulled right
	ld.AddLine(p2, p3, bottomRightBevelColor)
	ld.AddLine(shiftp(p2, 0, 1), shiftp(p3, 1, 1), bottomRightBevelColor)
	ld.AddLine(shiftp(p2, 0, 2), shiftp(p3, 2, 2), bottomRightBevelColor)

	// Scissor to just the extent of the button. Note that we need to give
	// this in window coordinates, not our local pane coordinates, so
	// translating by ctx.PaneExtent.p0 is needed...
	winBase := math.Add2f(dcbDrawState.cursor, ctx.PaneExtent.P0)
	dcbDrawState.cb.SetScissorBounds(math.Extent2D{
		P0: [2]float32{winBase[0], winBase[1] - sz[1]},
		P1: [2]float32{winBase[0] + sz[0], winBase[1]},
	}, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])

	updateDCBCursor(flags, sz, ctx)

	// Text last!
	trid.GenerateCommands(dcbDrawState.cb)
	ld.GenerateCommands(dcbDrawState.cb)
	td.GenerateCommands(dcbDrawState.cb)

	if mouse != nil && mouseInside && mouse.Released[platform.MouseButtonPrimary] && mouseDownInside {
		return ext, true /* clicked and released */
	}
	return ext, false
}

func updateDCBCursor(flags int, sz [2]float32, ctx *panes.Context) {
	if dcbDrawState.position == dcbPositionTop || dcbDrawState.position == dcbPositionBottom {
		// Drawing left to right
		if (flags&buttonFull) != 0 || (flags&buttonHalfHorizontal) != 0 {
			// For full height buttons, always go to the next column
			dcbDrawState.cursor[0] += sz[0]
			dcbDrawState.cursor[1] = dcbDrawState.drawStartPos[1]
		} else if (flags & buttonHalfVertical) != 0 {
			if dcbDrawState.cursor[1] == dcbDrawState.drawStartPos[1] {
				// Room for another half-height button below
				dcbDrawState.cursor[1] -= sz[1]
			} else {
				// On to the next column
				dcbDrawState.cursor[0] += sz[0]
				dcbDrawState.cursor[1] = dcbDrawState.drawStartPos[1]
			}
		} else {
			ctx.Lg.Errorf("unhandled starsButtonFlags %d", flags)
			dcbDrawState.cursor[0] += sz[0]
			dcbDrawState.cursor[1] = dcbDrawState.drawStartPos[1]
		}
	} else {
		// Drawing top to bottom
		if (flags&buttonFull) != 0 || (flags&buttonHalfVertical) != 0 {
			// For full width buttons, always go to the next row
			dcbDrawState.cursor[0] = dcbDrawState.drawStartPos[0]
			dcbDrawState.cursor[1] -= sz[1]
		} else if (flags & buttonHalfHorizontal) != 0 {
			if dcbDrawState.cursor[0] == dcbDrawState.drawStartPos[0] {
				// Room for another half-width button to the right
				dcbDrawState.cursor[0] += sz[0]
			} else {
				// On to the next row
				dcbDrawState.cursor[0] = dcbDrawState.drawStartPos[0]
				dcbDrawState.cursor[1] -= sz[1]
			}
		} else {
			ctx.Lg.Errorf("unhandled starsButtonFlags %d", flags)
			dcbDrawState.cursor[0] = dcbDrawState.drawStartPos[0]
			dcbDrawState.cursor[1] -= sz[0]
		}
	}
}

func toggleButton(ctx *panes.Context, text string, state *bool, flags int, buttonScale float32) bool {
	_, clicked := drawDCBButton(ctx, text, flags, buttonScale, *state, false)

	if clicked {
		*state = !*state
	}

	return clicked
}

// TODO: think about implications of multiple STARSPanes being active
// at once w.r.t. this.  This probably should be a member variable,
// though we also need to think about focus capture; probably should
// force take it when a spinner is active..
var activeSpinner dcbSpinner

// drawDCBSpinner draws the provided spinner at the current location in the
// DCB. It handles mouse capture (and release) and passing mouse wheel
// events to the spinner.
func (sp *STARSPane) drawDCBSpinner(ctx *panes.Context, spinner dcbSpinner, commandMode CommandMode, flags int, buttonScale float32) {
	if activeSpinner != nil && spinner.Equals(activeSpinner) {
		// This spinner is active.
		buttonBounds, clicked := drawDCBButton(ctx, spinner.Label(), flags, buttonScale, true, false)
		// This is horrific and one of many ugly things about capturing the
		// mouse, but most of Panes' work is in the simplified space of a
		// pane coordinate system; here we need something in terms of
		// window coordinates, so need to both account for the viewport
		// call that lets us draw things oblivious to the menubar as well
		// as flip things in y.
		h := ctx.PaneExtent.Height() + ctx.MenuBarHeight
		buttonBounds.P0[1], buttonBounds.P1[1] = h-buttonBounds.P1[1], h-buttonBounds.P0[1]
		ctx.Platform.StartCaptureMouse(buttonBounds)

		if clicked {
			sp.disableMenuSpinner(ctx)
		}

		if ctx.Mouse != nil && ctx.Mouse.Wheel[1] != 0 {
			delta := util.Select(ctx.Mouse.Wheel[1] > 0, -1, 1)
			spinner.MouseWheel(delta)
		}
	} else {
		// The spinner is not active; draw it (and check if it was clicked...)
		_, clicked := drawDCBButton(ctx, spinner.Label(), flags, buttonScale, false, false)
		if clicked {
			activeSpinner = spinner
			sp.resetInputState()
			sp.commandMode = commandMode
		}
	}
}

type dcbRadarRangeSpinner struct {
	r *float32
}

func makeRadarRangeSpinner(r *float32) dcbSpinner {
	return &dcbRadarRangeSpinner{r}
}

func (s dcbRadarRangeSpinner) Label() string {
	return "RANGE\n" + strconv.Itoa(int(*s.r+0.5)) // print it as an int
}

func (s dcbRadarRangeSpinner) Equals(other dcbSpinner) bool {
	r, ok := other.(*dcbRadarRangeSpinner)
	return ok && r.r == s.r
}

func (s *dcbRadarRangeSpinner) MouseWheel(delta int) {
	*s.r = math.Clamp(*s.r+float32(delta), 6, 256)
}

func (s *dcbRadarRangeSpinner) KeyboardInput(text string) error {
	// 4-33
	if r, err := strconv.Atoi(text); err != nil {
		return ErrSTARSCommandFormat
	} else if r < 6 || r > 256 {
		return ErrSTARSRangeLimit
	} else {
		// Input numbers are ints but we store a float (for smoother
		// stepping when the mouse wheel is used to zoom the scope).
		*s.r = float32(r)
		return nil
	}
}

func (s *dcbRadarRangeSpinner) Disabled() {}

// dcbIntegerRangeSpinner is a generic implementation of dcbSpinner for
// managing integers in steps of 1 within a given range.
type dcbIntegerRangeSpinner struct {
	text     string
	value    *int
	min, max int
}

func makeIntegerRangeSpinner(t string, v *int, min, max int) *dcbIntegerRangeSpinner {
	return &dcbIntegerRangeSpinner{text: t, value: v, min: min, max: max}
}

func (s *dcbIntegerRangeSpinner) Label() string {
	return s.text + strconv.Itoa(*s.value)
}

func (s *dcbIntegerRangeSpinner) Equals(other dcbSpinner) bool {
	ir, ok := other.(*dcbIntegerRangeSpinner)
	return ok && ir.value == s.value
}

func (s *dcbIntegerRangeSpinner) MouseWheel(delta int) {
	*s.value = math.Clamp(*s.value+delta, s.min, s.max)
}

func (s *dcbIntegerRangeSpinner) KeyboardInput(text string) error {
	if v, err := strconv.Atoi(text); err != nil {
		return ErrSTARSCommandFormat
	} else if v < s.min || v > s.max {
		return ErrSTARSRangeLimit
	} else {
		*s.value = v
		return nil
	}
}

func (s *dcbIntegerRangeSpinner) Disabled() {}

type dcbAudioVolumeSpinner struct {
	*dcbIntegerRangeSpinner
	p  platform.Platform
	sp *STARSPane
}

func (v *dcbAudioVolumeSpinner) Equals(other dcbSpinner) bool {
	vs, ok := other.(*dcbAudioVolumeSpinner)
	return ok && vs.value == v.value
}

func (s *dcbAudioVolumeSpinner) MouseWheel(delta int) {
	old := *s.value
	s.dcbIntegerRangeSpinner.MouseWheel(delta)
	if *s.value != old {
		s.p.SetAudioVolume(*s.value)
		s.p.StopPlayAudio(s.sp.audioEffects[AudioTest])
		s.p.PlayAudioOnce(s.sp.audioEffects[AudioTest])
	}
}

func (s *dcbAudioVolumeSpinner) KeyboardInput(text string) error {
	old := *s.value
	err := s.dcbIntegerRangeSpinner.KeyboardInput(text)
	if err == nil && *s.value != old {
		s.p.SetAudioVolume(*s.value)
		s.p.StopPlayAudio(s.sp.audioEffects[AudioTest])
		s.p.PlayAudioOnce(s.sp.audioEffects[AudioTest])
	}
	return err
}

func makeAudioVolumeSpinner(p platform.Platform, sp *STARSPane, vol *int) *dcbAudioVolumeSpinner {
	return &dcbAudioVolumeSpinner{
		dcbIntegerRangeSpinner: makeIntegerRangeSpinner("VOL\n", vol, 1, 10),
		p:                      p,
		sp:                     sp,
	}
}

// Leader lines are integers between 0 and 7 so the IntegerRangeSpinner
// fits.
func makeLeaderLineLengthSpinner(l *int) dcbSpinner {
	return makeIntegerRangeSpinner("LDR\n", l, 0, 7)
}

type dcbLeaderLineDirectionSpinner struct {
	sp *STARSPane
	d  *math.CardinalOrdinalDirection
}

func makeLeaderLineDirectionSpinner(sp *STARSPane, dir *math.CardinalOrdinalDirection) dcbSpinner {
	return &dcbLeaderLineDirectionSpinner{sp: sp, d: dir}
}

func (s *dcbLeaderLineDirectionSpinner) Label() string {
	return "LDR DIR\n" + s.d.ShortString()
}

func (s *dcbLeaderLineDirectionSpinner) Equals(other dcbSpinner) bool {
	l, ok := other.(*dcbLeaderLineDirectionSpinner)
	return ok && l.d == s.d
}

func (s *dcbLeaderLineDirectionSpinner) MouseWheel(delta int) {
	// The CardinalOrdinalDirection enum goes clockwise, so adding one (mod
	// 8) goes forward, and subtracting 1 (mod 8) goes backwards.
	if delta < 0 {
		*s.d = math.CardinalOrdinalDirection((*s.d + 7) % 8)
	} else {
		*s.d = math.CardinalOrdinalDirection((*s.d + 1) % 8)
	}
}

func (s *dcbLeaderLineDirectionSpinner) KeyboardInput(text string) error {
	if len(text) > 1 {
		return ErrSTARSCommandFormat
	} else if dir, ok := s.sp.numpadToDirection(text[0]); !ok || dir == nil /* entered 5 */ {
		return ErrSTARSCommandFormat
	} else {
		*s.d = *dir
		return nil
	}
}

func (s *dcbLeaderLineDirectionSpinner) Disabled() {}

type dcbHistoryRateSpinner struct {
	r *float32
}

func makeHistoryRateSpinner(r *float32) dcbSpinner {
	return &dcbHistoryRateSpinner{r}
}

func (s *dcbHistoryRateSpinner) Label() string {
	return "H_RATE\n" + fmt.Sprintf("%.1f", *s.r)
}

func (s *dcbHistoryRateSpinner) Equals(other dcbSpinner) bool {
	r, ok := other.(*dcbHistoryRateSpinner)
	return ok && r.r == s.r
}

func (s *dcbHistoryRateSpinner) MouseWheel(delta int) {
	// 4-94 the spinner goes in steps of 0.5.
	if delta > 0 {
		*s.r = math.Clamp(*s.r+0.5, 0, 4.5)
	} else if delta < 0 {
		*s.r = math.Clamp(*s.r-0.5, 0, 4.5)
	}
}

func (s *dcbHistoryRateSpinner) KeyboardInput(text string) error {
	// 4-94: however, for keyboard input, values in the range 0-4.5 in
	// increments of 0.1 are allowed.

	// Simple specialized parser to make it easier to validate (versus
	// using strconv.ParseFloat and then having to verify it's a valid
	// value.)
	whole, frac, ok := strings.Cut(text, ".")
	if !ok {
		frac = "0"
	}

	// Make sure we have a single digit for the whole part and the
	// fractional part.
	if len(whole) != 1 || whole[0] < '0' || whole[0] > '9' {
		return ErrSTARSIllegalValue
	}
	if len(frac) != 1 || frac[0] < '0' || frac[0] > '9' {
		return ErrSTARSIllegalValue
	}

	// Convert it to a float
	if value := float32(whole[0]-'0') + float32(frac[0]-'0')/10; value > 4.5 {
		return ErrSTARSIllegalValue
	} else {
		*s.r = value
		return nil
	}
}

func (s *dcbHistoryRateSpinner) Disabled() {}

type dcbPTLLengthSpinner struct {
	l *float32
}

func makePTLLengthSpinner(l *float32) dcbSpinner {
	return &dcbPTLLengthSpinner{l}
}

func (s *dcbPTLLengthSpinner) Label() string {
	return "PTL\nLNTH\n" + fmt.Sprintf("%.1f", *s.l)
}

func (s *dcbPTLLengthSpinner) Equals(other dcbSpinner) bool {
	p, ok := other.(*dcbPTLLengthSpinner)
	return ok && p.l == s.l
}

func (s *dcbPTLLengthSpinner) MouseWheel(delta int) {
	// 6-16: PTLs are between 0 and 5 minutes, specified in 0.5 minute
	// increments.
	if delta > 0 {
		*s.l = math.Min(*s.l+0.5, 5)
	} else if delta < 0 {
		*s.l = math.Max(*s.l-0.5, 0)
	}
}

func (s *dcbPTLLengthSpinner) KeyboardInput(text string) error {
	// Here we'll just parse it as a float and then validate it.
	if v, err := strconv.ParseFloat(text, 32); err != nil {
		return ErrSTARSCommandFormat
	} else if v < 0 || v > 5 {
		// out of range
		return ErrSTARSCommandFormat
	} else if float64(int(v)) != v && float64(int(v))+0.5 != v {
		// Not a whole number or a decimal x.5
		return ErrSTARSCommandFormat
	} else {
		*s.l = float32(v)
		return nil
	}
}

func (s *dcbPTLLengthSpinner) Disabled() {}

type dcbDwellModeSpinner struct {
	m *DwellMode
}

func makeDwellModeSpinner(m *DwellMode) dcbSpinner {
	return &dcbDwellModeSpinner{m}
}

func (s *dcbDwellModeSpinner) Label() string {
	return "DWELL\n" + s.m.String()
}

func (s *dcbDwellModeSpinner) Equals(other dcbSpinner) bool {
	d, ok := other.(*dcbDwellModeSpinner)
	return ok && s.m == d.m
}

func (s *dcbDwellModeSpinner) MouseWheel(delta int) {
	if delta > 0 {
		// Cycle through the modes Off -> On -> Lock
		*s.m = [3]DwellMode{DwellModeOff: DwellModeOn,
			DwellModeOn:   DwellModeLock,
			DwellModeLock: DwellModeLock}[*s.m]
	} else if delta < 0 {
		// Cycle: Lock-> On -> Off
		*s.m = [3]DwellMode{DwellModeOff: DwellModeOff,
			DwellModeOn:   DwellModeOff,
			DwellModeLock: DwellModeOn}[*s.m]
	}
}

func (s *dcbDwellModeSpinner) KeyboardInput(text string) error {
	// 4-109
	switch text {
	case "0":
		*s.m = DwellModeOff
		return nil
	case "1":
		*s.m = DwellModeOn
		return nil
	case "2":
		*s.m = DwellModeLock
		return nil
	default:
		return ErrSTARSIllegalValue
	}
}

func (s *dcbDwellModeSpinner) Disabled() {}

type dcbRangeRingRadiusSpinner struct {
	r *int
}

func makeRangeRingRadiusSpinner(radius *int) dcbSpinner {
	return &dcbRangeRingRadiusSpinner{radius}
}

func (s *dcbRangeRingRadiusSpinner) Label() string {
	return "RR\n" + strconv.Itoa(*s.r)
}

func (s *dcbRangeRingRadiusSpinner) Equals(other dcbSpinner) bool {
	r, ok := other.(*dcbRangeRingRadiusSpinner)
	return ok && r.r == s.r
}

func (s *dcbRangeRingRadiusSpinner) MouseWheel(delta int) {
	// Range rings have 2, 5, 10, or 20 miles radii..
	if delta > 0 {
		switch *s.r {
		case 2:
			*s.r = 5
		case 5:
			*s.r = 10
		case 10:
			*s.r = 20
		}
	} else {
		switch *s.r {
		case 5:
			*s.r = 2
		case 10:
			*s.r = 5
		case 20:
			*s.r = 10
		}
	}
}

func (s *dcbRangeRingRadiusSpinner) KeyboardInput(text string) error {
	if v, err := strconv.Atoi(text); err != nil {
		return ErrSTARSCommandFormat
	} else if v != 2 && v != 5 && v != 10 && v != 20 {
		return ErrSTARSIllegalValue
	} else {
		*s.r = v
		return nil
	}
}

func (s *dcbRangeRingRadiusSpinner) Disabled() {}

// dcbBrightnessSpinner handles spinners in the BRITE menu
type dcbBrightnessSpinner struct {
	text     string
	b        *STARSBrightness
	min      STARSBrightness
	allowOff bool
}

func makeBrightnessSpinner(t string, b *STARSBrightness, min STARSBrightness, allowOff bool) dcbSpinner {
	return &dcbBrightnessSpinner{text: t, b: b, min: min, allowOff: allowOff}
}

func (s *dcbBrightnessSpinner) Label() string {
	return s.text + " " + util.Select(*s.b == 0, "OFF", fmt.Sprintf("%2d", int(*s.b)))
}

func (s *dcbBrightnessSpinner) Equals(other dcbSpinner) bool {
	b, ok := other.(*dcbBrightnessSpinner)
	return ok && b.b == s.b
}

func (s *dcbBrightnessSpinner) MouseWheel(delta int) {
	*s.b += STARSBrightness(5 * delta)
	if *s.b < s.min && s.allowOff {
		*s.b = STARSBrightness(0)
	} else {
		*s.b = STARSBrightness(math.Clamp(*s.b, s.min, 100))
	}
}

func (s *dcbBrightnessSpinner) KeyboardInput(text string) error {
	if v, err := strconv.Atoi(text); err != nil {
		return ErrSTARSCommandFormat
	} else if v < int(s.min) || v > 100 || (v == 0 && !s.allowOff) {
		return ErrSTARSIllegalValue
	} else {
		*s.b = STARSBrightness(v)
		return nil
	}
}

func (s *dcbBrightnessSpinner) Disabled() {}

func selectButton(ctx *panes.Context, text string, flags int, buttonScale float32) bool {
	_, clicked := drawDCBButton(ctx, text, flags, buttonScale, flags&buttonSelected != 0, false)
	return clicked
}

func (sp *STARSPane) placeButton(ctx *panes.Context, text string, flags int, buttonScale float32,
	callback func(pw [2]float32, transforms ScopeTransformations) CommandStatus) {
	_, clicked := drawDCBButton(ctx, text, flags, buttonScale, text == sp.selectedPlaceButton, false)
	if clicked {
		sp.selectedPlaceButton = text
		sp.scopeClickHandler = func(pw [2]float32, transforms ScopeTransformations) CommandStatus {
			sp.selectedPlaceButton = ""
			return callback(pw, transforms)
		}
	}
}

func disabledButton(ctx *panes.Context, text string, flags int, buttonScale float32) {
	drawDCBButton(ctx, text, flags, buttonScale, false, true)
}

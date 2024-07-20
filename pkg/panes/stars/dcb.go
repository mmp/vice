// pkg/panes/stars/dcb.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
)

var (
	dcbButtonColor         = renderer.RGB{0, .173, 0}
	dcbActiveButtonColor   = renderer.RGB{0, .305, 0}
	dcbTextColor           = renderer.RGB{1, 1, 1}
	dcbTextSelectedColor   = renderer.RGB{1, 1, 0}
	dcbDisabledButtonColor = renderer.RGB{.4, .4, .4}
	dcbDisabledTextColor   = renderer.RGB{.8, .8, .8}
)

const dcbButtonSize = 76

const (
	buttonFull = 1 << iota
	buttonHalfVertical
	buttonHalfHorizontal
	buttonSelected
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

// DCBSpinner is an interface used to manage the various spinner types in
// the DCB menu. Since the various spinners generally have unique
// requirements and expectations about keyboard input, having this
// interface allows collecting all of that in the various implementations
// of the interface.
type DCBSpinner interface {
	// Label returns the text that should be shown in the DCB button.
	Label() string
	// Equal returns true if the provided spinner controls the same value
	// as this spinner.
	Equals(other DCBSpinner) bool
	// MouseWheel is called when the spinner is active and there is mouse
	// wheel input; implementations should update the underlying value
	// accordingly.
	MouseWheel(delta int)
	// KeyboardInput is called if the spinner is active and the user enters
	// text and presses enter; implementations should update the underlying
	// value accordingly.
	KeyboardInput(text string) error
}

func (sp *STARSPane) disableMenuSpinner(ctx *panes.Context) {
	activeSpinner = nil
	ctx.Platform.EndCaptureMouse()
	sp.commandMode = CommandModeNone
}

func (sp *STARSPane) activateMenuSpinner(spinner DCBSpinner) {
	activeSpinner = spinner
}

func (sp *STARSPane) DrawDCB(ctx *panes.Context, transforms ScopeTransformations, cb *renderer.CommandBuffer) math.Extent2D {
	ps := &sp.CurrentPreferenceSet

	// Find a scale factor so that the buttons all fit in the window, if necessary
	const NumDCBSlots = 20
	// Sigh; on windows we want the button size in pixels on high DPI displays
	ds := util.Select(runtime.GOOS == "windows", ctx.Platform.DPIScale(), float32(1))
	var buttonScale float32
	// Scale based on width or height available depending on DCB position
	if ps.DCBPosition == dcbPositionTop || ps.DCBPosition == dcbPositionBottom {
		buttonScale = math.Min(ds, (ds*ctx.PaneExtent.Width()-4)/(NumDCBSlots*dcbButtonSize))
	} else {
		buttonScale = math.Min(ds, (ds*ctx.PaneExtent.Height()-4)/(NumDCBSlots*dcbButtonSize))
	}

	sp.startDrawDCB(ctx, buttonScale, transforms, cb)

	switch sp.activeDCBMenu {
	case dcbMenuMain:
		sp.DrawDCBSpinner(ctx, MakeRadarRangeSpinner(&ps.Range), CommandModeRange,
			buttonFull, buttonScale)
		sp.STARSPlaceButton(ctx, "PLACE\nCNTR", buttonHalfVertical, buttonScale,
			func(pw [2]float32, transforms ScopeTransformations) (status CommandStatus) {
				ps.Center = transforms.LatLongFromWindowP(pw)
				ps.CurrentCenter = ps.Center
				sp.weatherRadar.UpdateCenter(ps.Center)
				status.clear = true
				return
			})
		ps.OffCenter = ps.CurrentCenter != ps.Center
		if STARSToggleButton(ctx, "OFF\nCNTR", &ps.OffCenter, buttonHalfVertical, buttonScale) {
			ps.CurrentCenter = ps.Center
		}
		sp.DrawDCBSpinner(ctx, MakeRangeRingRadiusSpinner(&ps.RangeRingRadius), CommandModeRangeRings,
			buttonFull, buttonScale)
		sp.STARSPlaceButton(ctx, "PLACE\nRR", buttonHalfVertical, buttonScale,
			func(pw [2]float32, transforms ScopeTransformations) (status CommandStatus) {
				ps.RangeRingsCenter = transforms.LatLongFromWindowP(pw)
				status.clear = true
				return
			})
		if STARSSelectButton(ctx, "RR\nCNTR", buttonHalfVertical, buttonScale) {
			cw := [2]float32{ctx.PaneExtent.Width() / 2, ctx.PaneExtent.Height() / 2}
			ps.RangeRingsCenter = transforms.LatLongFromWindowP(cw)
		}
		if STARSSelectButton(ctx, "MAPS", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuMaps
		}
		videoMaps, _ := ctx.ControlClient.GetVideoMaps()
		for i := 0; i < 6; i++ {
			// Maps are given left->right, top->down, but we draw the
			// buttons top->down, left->right, so the indexing is a little
			// funny.
			idx := util.Select(i&1 == 0, i/2, 3+i/2)
			m := videoMaps[idx]
			text := util.Select(m.Id == 0, "", fmt.Sprintf("%d\n%s", m.Id, m.Label))
			STARSToggleButton(ctx, text, &ps.DisplayVideoMap[idx], buttonHalfVertical, buttonScale)
		}
		for i := range ps.DisplayWeatherLevel {
			STARSToggleButton(ctx, "WX"+strconv.Itoa(i), &ps.DisplayWeatherLevel[i], buttonHalfHorizontal, buttonScale)
		}
		if STARSSelectButton(ctx, "BRITE", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuBrite
		}
		sp.DrawDCBSpinner(ctx, MakeLeaderLineDirectionSpinner(&ps.LeaderLineDirection), CommandModeNone,
			buttonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeLeaderLineLengthSpinner(&ps.LeaderLineLength), CommandModeLDR,
			buttonHalfVertical, buttonScale)

		if STARSSelectButton(ctx, "CHAR\nSIZE", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuCharSize
		}
		STARSDisabledButton(ctx, "MODE\nFSL", buttonFull, buttonScale)
		if STARSSelectButton(ctx, "PREF\n"+ps.Name, buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuPref
		}

		site := sp.radarSiteId(ctx.ControlClient.RadarSites)
		if len(ctx.ControlClient.RadarSites) == 0 {
			STARSDisabledButton(ctx, "SITE\n"+site, buttonFull, buttonScale)
		} else {
			if STARSSelectButton(ctx, "SITE\n"+site, buttonFull, buttonScale) {
				sp.activeDCBMenu = dcbMenuSite
			}
		}
		if STARSSelectButton(ctx, "SSA\nFILTER", buttonHalfVertical, buttonScale) {
			sp.activeDCBMenu = dcbMenuSSAFilter
		}
		if STARSSelectButton(ctx, "GI TEXT\nFILTER", buttonHalfVertical, buttonScale) {
			sp.activeDCBMenu = dcbMenuGITextFilter
		}
		if STARSSelectButton(ctx, "SHIFT", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuAux
		}

	case dcbMenuAux:
		STARSDisabledButton(ctx, "VOL\n10", buttonFull, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeIntegerRangeSpinner("HISTORY\n", &ps.RadarTrackHistory, 0, 10),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeHistoryRateSpinner(&ps.RadarTrackHistoryRate),
			CommandModeNone, buttonHalfVertical, buttonScale)
		STARSDisabledButton(ctx, "CURSOR\nHOME", buttonFull, buttonScale)
		STARSDisabledButton(ctx, "CSR SPD\n4", buttonFull, buttonScale)
		STARSDisabledButton(ctx, "MAP\nUNCOR", buttonFull, buttonScale)
		STARSDisabledButton(ctx, "UNCOR", buttonFull, buttonScale)
		STARSDisabledButton(ctx, "BEACON\nMODE-2", buttonFull, buttonScale)
		STARSDisabledButton(ctx, "RTQC", buttonFull, buttonScale)
		STARSDisabledButton(ctx, "MCP", buttonFull, buttonScale)
		top := ps.DCBPosition == dcbPositionTop
		if STARSToggleButton(ctx, "DCB\nTOP", &top, buttonHalfVertical, buttonScale) {
			ps.DCBPosition = dcbPositionTop
		}
		left := ps.DCBPosition == dcbPositionLeft
		if STARSToggleButton(ctx, "DCB\nLEFT", &left, buttonHalfVertical, buttonScale) {
			ps.DCBPosition = dcbPositionLeft
		}
		right := ps.DCBPosition == dcbPositionRight
		if STARSToggleButton(ctx, "DCB\nRIGHT", &right, buttonHalfVertical, buttonScale) {
			ps.DCBPosition = dcbPositionRight
		}
		bottom := ps.DCBPosition == dcbPositionBottom
		if STARSToggleButton(ctx, "DCB\nBOTTOM", &bottom, buttonHalfVertical, buttonScale) {
			ps.DCBPosition = dcbPositionBottom
		}
		sp.DrawDCBSpinner(ctx, MakePTLLengthSpinner(&ps.PTLLength), CommandModeNone, buttonFull, buttonScale)
		if ps.PTLLength > 0 {
			if STARSToggleButton(ctx, "PTL OWN", &ps.PTLOwn, buttonHalfVertical, buttonScale) && ps.PTLOwn {
				ps.PTLAll = false
			}
			if STARSToggleButton(ctx, "PTL ALL", &ps.PTLAll, buttonHalfVertical, buttonScale) && ps.PTLAll {
				ps.PTLOwn = false
			}
		} else {
			STARSDisabledButton(ctx, "PTL OWN", buttonHalfVertical, buttonScale)
			STARSDisabledButton(ctx, "PTL ALL", buttonHalfVertical, buttonScale)

		}
		sp.DrawDCBSpinner(ctx, MakeDwellModeSpinner(&ps.DwellMode), CommandModeNone, buttonFull, buttonScale)
		if STARSSelectButton(ctx, "TPA/\nATPA", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuTPA
		}
		if STARSSelectButton(ctx, "SHIFT", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuMain
		}

	case dcbMenuMaps:
		if STARSSelectButton(ctx, "DONE", buttonHalfVertical, buttonScale) {
			sp.activeDCBMenu = dcbMenuMain
		}
		if STARSSelectButton(ctx, "CLR ALL", buttonHalfVertical, buttonScale) {
			for i := range ps.DisplayVideoMap {
				ps.DisplayVideoMap[i] = false
			}
			ps.SystemMapVisible = make(map[int]interface{})
		}
		videoMaps, _ := ctx.ControlClient.GetVideoMaps()
		for i := 0; i < sim.NumSTARSMaps-6; i++ {
			// Indexing is tricky both because we are skipping the first 6
			// maps, which are shown in the main DCB, but also because we
			// draw top->down, left->right while the maps are specified
			// left->right, top->down...
			idx := util.Select(i&1 == 0, 6+i/2, 22+i/2)
			m := videoMaps[idx]
			text := util.Select(m.Id == 0, "", fmt.Sprintf("%d\n%s", m.Id, m.Label))
			STARSToggleButton(ctx, text, &ps.DisplayVideoMap[idx], buttonHalfVertical, buttonScale)
		}

		geoMapsSelected := ps.VideoMapsList.Selection == VideoMapsGroupGeo && ps.VideoMapsList.Visible
		if STARSToggleButton(ctx, "GEO\nMAPS", &geoMapsSelected, buttonHalfVertical, buttonScale) {
			ps.VideoMapsList.Selection = VideoMapsGroupGeo
			ps.VideoMapsList.Visible = geoMapsSelected
		}
		STARSDisabledButton(ctx, "AIRPORT", buttonHalfVertical, buttonScale)
		sysProcSelected := ps.VideoMapsList.Selection == VideoMapsGroupSysProc && ps.VideoMapsList.Visible
		if STARSToggleButton(ctx, "SYS\nPROC", &sysProcSelected, buttonHalfVertical, buttonScale) {
			ps.VideoMapsList.Selection = VideoMapsGroupSysProc
			ps.VideoMapsList.Visible = sysProcSelected
		}
		currentMapsSelected := ps.VideoMapsList.Selection == VideoMapsGroupCurrent && ps.VideoMapsList.Visible
		if STARSToggleButton(ctx, "CURRENT", &currentMapsSelected, buttonHalfVertical, buttonScale) {
			ps.VideoMapsList.Selection = VideoMapsGroupCurrent
			ps.VideoMapsList.Visible = currentMapsSelected
		}

	case dcbMenuBrite:
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("DCB", &ps.Brightness.DCB, 25, false),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("BKC", &ps.Brightness.BackgroundContrast, 0, false),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("MPA", &ps.Brightness.VideoGroupA, 5, false),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("MPB", &ps.Brightness.VideoGroupB, 5, false),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("FDB", &ps.Brightness.FullDatablocks, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("LST", &ps.Brightness.Lists, 25, false),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("POS", &ps.Brightness.Positions, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("LDB", &ps.Brightness.LimitedDatablocks, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("OTH", &ps.Brightness.OtherTracks, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("TLS", &ps.Brightness.Lines, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("RR", &ps.Brightness.RangeRings, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("CMP", &ps.Brightness.Compass, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("BCN", &ps.Brightness.BeaconSymbols, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("PRI", &ps.Brightness.PrimarySymbols, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("HST", &ps.Brightness.History, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		// The STARS manual, p.4-74 actually says that weather can't go to OFF... FIXME?
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("WX", &ps.Brightness.Weather, 5, true),
			CommandModeNone, buttonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("WXC", &ps.Brightness.WxContrast, 5, false),
			CommandModeNone, buttonHalfVertical, buttonScale)
		if ps.Brightness.Weather != 0 {
			sp.weatherRadar.Activate(sp.CurrentPreferenceSet.Center, ctx.Renderer, ctx.Lg)
		} else {
			// Don't fetch weather maps if they're not going to be displayed.
			sp.weatherRadar.Deactivate()
		}
		if STARSSelectButton(ctx, "DONE", buttonHalfVertical, buttonScale) {
			sp.activeDCBMenu = dcbMenuMain
		}

	case dcbMenuCharSize:
		sp.DrawDCBSpinner(ctx, MakeIntegerRangeSpinner("DATA\nBLOCKS\n", &ps.CharSize.Datablocks, 0, 5),
			CommandModeNone, buttonFull, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeIntegerRangeSpinner("LISTS\n", &ps.CharSize.Lists, 0, 5),
			CommandModeNone, buttonFull, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeIntegerRangeSpinner("DCB\n", &ps.CharSize.DCB, 0, 2),
			CommandModeNone, buttonFull, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeIntegerRangeSpinner("TOOLS\n", &ps.CharSize.Tools, 0, 5),
			CommandModeNone, buttonFull, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeIntegerRangeSpinner("POS\n", &ps.CharSize.PositionSymbols, 0, 5),
			CommandModeNone, buttonFull, buttonScale)
		if STARSSelectButton(ctx, "DONE", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuMain
		}

	case dcbMenuPref:
		for i := range sp.PreferenceSets {
			text := fmt.Sprintf("%d\n%s", i+1, sp.PreferenceSets[i].Name)
			flags := buttonHalfVertical
			if i == sp.SelectedPreferenceSet {
				flags = flags | buttonSelected
			}
			if STARSSelectButton(ctx, text, flags, buttonScale) {
				// Make this one current
				sp.SelectedPreferenceSet = i
				sp.CurrentPreferenceSet = sp.PreferenceSets[i]
				sp.weatherRadar.Activate(sp.CurrentPreferenceSet.Center, ctx.Renderer, ctx.Lg)
			}
		}
		for i := len(sp.PreferenceSets); i < NumPreferenceSets; i++ {
			STARSDisabledButton(ctx, fmt.Sprintf("%d\n", i+1), buttonHalfVertical, buttonScale)
		}

		if STARSSelectButton(ctx, "DEFAULT", buttonHalfVertical, buttonScale) {
			sp.CurrentPreferenceSet = sp.MakePreferenceSet("", &ctx.ControlClient.State)
		}
		STARSDisabledButton(ctx, "FSSTARS", buttonHalfVertical, buttonScale)
		if STARSSelectButton(ctx, "RESTORE", buttonHalfVertical, buttonScale) {
			// TODO: restore settings in effect when entered the Pref sub-menu
		}

		validSelection := sp.SelectedPreferenceSet != -1 && sp.SelectedPreferenceSet < len(sp.PreferenceSets)
		if validSelection {
			if STARSSelectButton(ctx, "SAVE", buttonHalfVertical, buttonScale) {
				sp.PreferenceSets[sp.SelectedPreferenceSet] = sp.CurrentPreferenceSet
				// FIXME? globalConfig.Save()
			}
		} else {
			STARSDisabledButton(ctx, "SAVE", buttonHalfVertical, buttonScale)
		}
		STARSDisabledButton(ctx, "CHG PIN", buttonHalfVertical, buttonScale)
		if STARSSelectButton(ctx, "SAVE AS", buttonHalfVertical, buttonScale) {
			// A command mode handles prompting for the name and then saves
			// when enter is pressed.
			sp.commandMode = CommandModeSavePrefAs
		}
		if validSelection {
			if STARSSelectButton(ctx, "DELETE", buttonHalfVertical, buttonScale) {
				sp.PreferenceSets = util.DeleteSliceElement(sp.PreferenceSets, sp.SelectedPreferenceSet)
			}
		} else {
			STARSDisabledButton(ctx, "DELETE", buttonHalfVertical, buttonScale)
		}

		if STARSSelectButton(ctx, "DONE", buttonHalfVertical, buttonScale) {
			sp.activeDCBMenu = dcbMenuMain
		}

	case dcbMenuSite:
		for _, id := range util.SortedMapKeys(ctx.ControlClient.RadarSites) {
			site := ctx.ControlClient.RadarSites[id]
			label := " " + site.Char + " " + "\n" + id
			selected := ps.RadarSiteSelected == id
			if STARSToggleButton(ctx, label, &selected, buttonFull, buttonScale) {
				if selected {
					ps.RadarSiteSelected = id
				} else {
					ps.RadarSiteSelected = ""
				}
			}
		}
		// Fill extras with empty disabled buttons
		for i := len(ctx.ControlClient.RadarSites); i < 15; i++ {
			STARSDisabledButton(ctx, "", buttonFull, buttonScale)
		}
		multi := sp.radarMode(ctx.ControlClient.RadarSites) == RadarModeMulti
		if STARSToggleButton(ctx, "MULTI", &multi, buttonFull, buttonScale) && multi {
			ps.RadarSiteSelected = ""
			if ps.FusedRadarMode {
				sp.discardTracks = true
			}
			ps.FusedRadarMode = false
		}
		fused := sp.radarMode(ctx.ControlClient.RadarSites) == RadarModeFused
		if STARSToggleButton(ctx, "FUSED", &fused, buttonFull, buttonScale) && fused {
			ps.RadarSiteSelected = ""
			ps.FusedRadarMode = true
			sp.discardTracks = true
		}
		if STARSSelectButton(ctx, "DONE", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuMain
		}

	case dcbMenuSSAFilter:
		STARSToggleButton(ctx, "ALL", &ps.SSAList.Filter.All, buttonHalfVertical, buttonScale)
		STARSDisabledButton(ctx, "WX", buttonHalfVertical, buttonScale) // ?? TODO
		STARSToggleButton(ctx, "TIME", &ps.SSAList.Filter.Time, buttonHalfVertical, buttonScale)
		STARSToggleButton(ctx, "ALTSTG", &ps.SSAList.Filter.Altimeter, buttonHalfVertical, buttonScale)
		STARSToggleButton(ctx, "STATUS", &ps.SSAList.Filter.Status, buttonHalfVertical, buttonScale)
		STARSDisabledButton(ctx, "PLAN", buttonHalfVertical, buttonScale) // ?? TODO
		STARSToggleButton(ctx, "RADAR", &ps.SSAList.Filter.Radar, buttonHalfVertical, buttonScale)
		STARSToggleButton(ctx, "CODES", &ps.SSAList.Filter.Codes, buttonHalfVertical, buttonScale)
		STARSToggleButton(ctx, "SPC", &ps.SSAList.Filter.SpecialPurposeCodes, buttonHalfVertical, buttonScale)
		STARSDisabledButton(ctx, "SYS OFF", buttonHalfVertical, buttonScale) // ?? TODO
		STARSToggleButton(ctx, "RANGE", &ps.SSAList.Filter.Range, buttonHalfVertical, buttonScale)
		STARSToggleButton(ctx, "PTL", &ps.SSAList.Filter.PredictedTrackLines, buttonHalfVertical, buttonScale)
		STARSToggleButton(ctx, "ALT FIL", &ps.SSAList.Filter.AltitudeFilters, buttonHalfVertical, buttonScale)
		STARSDisabledButton(ctx, "NAS I/F", buttonHalfVertical, buttonScale) // ?? TODO
		STARSToggleButton(ctx, "AIRPORT", &ps.SSAList.Filter.AirportWeather, buttonHalfVertical, buttonScale)
		STARSDisabledButton(ctx, "OP MODE", buttonHalfVertical, buttonScale) // ?? TODO
		STARSDisabledButton(ctx, "TT", buttonHalfVertical, buttonScale)      // ?? TODO
		STARSDisabledButton(ctx, "WX HIST", buttonHalfVertical, buttonScale) // ?? TODO
		STARSToggleButton(ctx, "QL", &ps.SSAList.Filter.QuickLookPositions, buttonHalfVertical, buttonScale)
		STARSToggleButton(ctx, "TW OFF", &ps.SSAList.Filter.DisabledTerminal, buttonHalfVertical, buttonScale)
		STARSDisabledButton(ctx, "CON/CPL", buttonHalfVertical, buttonScale) // ?? TODO
		STARSDisabledButton(ctx, "OFF IND", buttonHalfVertical, buttonScale) // ?? TODO
		STARSToggleButton(ctx, "CRDA", &ps.SSAList.Filter.ActiveCRDAPairs, buttonHalfVertical, buttonScale)
		STARSDisabledButton(ctx, "", buttonHalfVertical, buttonScale)
		if STARSSelectButton(ctx, "DONE", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuMain
		}

	case dcbMenuGITextFilter:
		STARSToggleButton(ctx, "MAIN", &ps.SSAList.Filter.Text.Main, buttonHalfVertical, buttonScale)
		for i := range ps.SSAList.Filter.Text.GI {
			STARSToggleButton(ctx, fmt.Sprintf("GI %d", i+1), &ps.SSAList.Filter.Text.GI[i],
				buttonHalfVertical, buttonScale)
		}
		if STARSSelectButton(ctx, "DONE", buttonFull, buttonScale) {
			sp.activeDCBMenu = dcbMenuMain
		}

	case dcbMenuTPA:
		onoff := func(b bool) string { return util.Select(b, "ENABLED", "INHIBTD") }
		if STARSSelectButton(ctx, "A/TPA\nMILEAGE\n"+onoff(ps.DisplayTPASize), buttonFull, buttonScale) {
			ps.DisplayTPASize = !ps.DisplayTPASize
		}
		if STARSSelectButton(ctx, "INTRAIL\nDIST\n"+onoff(ps.DisplayATPAInTrailDist), buttonFull, buttonScale) {
			ps.DisplayATPAInTrailDist = !ps.DisplayATPAInTrailDist
		}
		if STARSSelectButton(ctx, "ALERT\nCONES\n"+onoff(ps.DisplayATPAWarningAlertCones), buttonFull, buttonScale) {
			ps.DisplayATPAWarningAlertCones = !ps.DisplayATPAWarningAlertCones
		}
		if STARSSelectButton(ctx, "MONITOR\nCONES\n"+onoff(ps.DisplayATPAMonitorCones), buttonFull, buttonScale) {
			ps.DisplayATPAMonitorCones = !ps.DisplayATPAMonitorCones
		}
		if STARSSelectButton(ctx, "DONE", buttonFull, buttonScale) {
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

	ps := sp.CurrentPreferenceSet
	dcbDrawState.brightness = ps.Brightness.DCB
	dcbDrawState.position = ps.DCBPosition
	switch dcbDrawState.position {
	case dcbPositionTop, dcbPositionLeft:
		dcbDrawState.drawStartPos = [2]float32{0, ctx.PaneExtent.Height()}

	case dcbPositionRight:
		sz := buttonSize(buttonFull, buttonScale) // FIXME: there should be a better way to get the default
		dcbDrawState.drawStartPos = [2]float32{ctx.PaneExtent.Width() - sz[0], ctx.PaneExtent.Height()}

	case dcbPositionBottom:
		sz := buttonSize(buttonFull, buttonScale)
		dcbDrawState.drawStartPos = [2]float32{0, sz[1]}
	}

	dcbDrawState.cursor = dcbDrawState.drawStartPos

	dcbDrawState.style = renderer.TextStyle{
		Font:        sp.dcbFont[ps.CharSize.DCB],
		Color:       renderer.RGB{1, 1, 1},
		LineSpacing: 0,
	}
	if dcbDrawState.style.Font == nil {
		ctx.Lg.Errorf("nil buttonFont??")
		dcbDrawState.style.Font = renderer.GetDefaultFont()
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.Platform.DPIScale())

	if ctx.Mouse != nil && ctx.Mouse.Clicked[platform.MouseButtonPrimary] {
		dcbDrawState.mouseDownPos = ctx.Mouse.Pos[:]
	}

	/*
		imgui.PushStyleColor(imgui.StyleColorText, imgui.Vec4{.7, .7, .7, 1})
		imgui.PushStyleColor(imgui.StyleColorButton, imgui.Vec4{.075, .075, .075, 1})
		imgui.PushStyleColor(imgui.StyleColorButtonHovered, imgui.Vec4{.3, .3, .3, 1})
		imgui.PushStyleColor(imgui.StyleColorButtonActive, imgui.Vec4{0, .2, 0, 1})
	*/
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
		buttonColor = util.Select(pushedIn, dcbActiveButtonColor, dcbButtonColor)
		textColor = util.Select(mouseInside, dcbTextSelectedColor, dcbTextColor)
	}
	buttonColor = dcbDrawState.brightness.ScaleRGB(buttonColor)
	//textColor = dcbDrawState.brightness.ScaleRGB(textColor)

	trid.AddQuad(p0, p1, p2, p3, buttonColor)
	drawDCBText(text, td, sz, textColor)

	if !disabled && pushedIn { //((selected && !mouseInside) || (!selected && mouseInside && mouse.Down[MouseButtonPrimary])) {
		// Depressed bevel scheme: darker top/left, highlight bottom/right
		ld.AddLine(p0, p1, renderer.LerpRGB(.5, buttonColor, renderer.RGB{0, 0, 0}))
		ld.AddLine(p0, p3, renderer.LerpRGB(.5, buttonColor, renderer.RGB{0, 0, 0}))
		ld.AddLine(p1, p2, renderer.LerpRGB(.25, buttonColor, renderer.RGB{1, 1, 1}))
		ld.AddLine(p2, p3, renderer.LerpRGB(.25, buttonColor, renderer.RGB{1, 1, 1}))
	} else {
		// Normal bevel scheme: highlight top and left, darker bottom and right
		ld.AddLine(p0, p1, renderer.LerpRGB(.25, buttonColor, renderer.RGB{1, 1, 1}))
		ld.AddLine(p0, p3, renderer.LerpRGB(.25, buttonColor, renderer.RGB{1, 1, 1}))
		ld.AddLine(p1, p2, renderer.LerpRGB(.5, buttonColor, renderer.RGB{0, 0, 0}))
		ld.AddLine(p2, p3, renderer.LerpRGB(.5, buttonColor, renderer.RGB{0, 0, 0}))
	}

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

func STARSToggleButton(ctx *panes.Context, text string, state *bool, flags int, buttonScale float32) bool {
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
var activeSpinner DCBSpinner

// DrawDCBSpinner draws the provided spinner at the current location in the
// DCB. It handles mouse capture (and release) and passing mouse wheel
// events to the spinner.
func (sp *STARSPane) DrawDCBSpinner(ctx *panes.Context, spinner DCBSpinner, commandMode CommandMode, flags int, buttonScale float32) {
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
			activeSpinner = nil
			ctx.Platform.EndCaptureMouse()
			sp.commandMode = CommandModeNone
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

type DCBRadarRangeSpinner struct {
	r *float32
}

func MakeRadarRangeSpinner(r *float32) DCBSpinner {
	return &DCBRadarRangeSpinner{r}
}

func (s DCBRadarRangeSpinner) Label() string {
	return "RANGE\n" + strconv.Itoa(int(*s.r+0.5)) // print it as an int
}

func (s DCBRadarRangeSpinner) Equals(other DCBSpinner) bool {
	r, ok := other.(*DCBRadarRangeSpinner)
	return ok && r.r == s.r
}

func (s *DCBRadarRangeSpinner) MouseWheel(delta int) {
	*s.r = math.Clamp(*s.r+float32(delta), 6, 256)
}

func (s *DCBRadarRangeSpinner) KeyboardInput(text string) error {
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

// DCBIntegerRangeSpinner is a generic implementation of DCBSpinner for
// managing integers in steps of 1 within a given range.
type DCBIntegerRangeSpinner struct {
	text     string
	value    *int
	min, max int
}

func MakeIntegerRangeSpinner(t string, v *int, min, max int) DCBSpinner {
	return &DCBIntegerRangeSpinner{text: t, value: v, min: min, max: max}
}

func (s *DCBIntegerRangeSpinner) Label() string {
	return s.text + strconv.Itoa(*s.value)
}

func (s *DCBIntegerRangeSpinner) Equals(other DCBSpinner) bool {
	ir, ok := other.(*DCBIntegerRangeSpinner)
	return ok && ir.value == s.value
}

func (s *DCBIntegerRangeSpinner) MouseWheel(delta int) {
	*s.value = math.Clamp(*s.value+delta, s.min, s.max)
}

func (s *DCBIntegerRangeSpinner) KeyboardInput(text string) error {
	if v, err := strconv.Atoi(text); err != nil {
		return ErrSTARSCommandFormat
	} else if v < s.min || v > s.max {
		return ErrSTARSRangeLimit
	} else {
		*s.value = v
		return nil
	}
}

// Leader lines are integers between 0 and 7 so the IntegerRangeSpinner
// fits.
func MakeLeaderLineLengthSpinner(l *int) DCBSpinner {
	return MakeIntegerRangeSpinner("LDR\n", l, 0, 7)
}

type DCBLeaderLineDirectionSpinner struct {
	d *math.CardinalOrdinalDirection
}

func MakeLeaderLineDirectionSpinner(dir *math.CardinalOrdinalDirection) DCBSpinner {
	return &DCBLeaderLineDirectionSpinner{dir}
}

func (s *DCBLeaderLineDirectionSpinner) Label() string {
	return "LDR DIR\n" + s.d.ShortString()
}

func (s *DCBLeaderLineDirectionSpinner) Equals(other DCBSpinner) bool {
	l, ok := other.(*DCBLeaderLineDirectionSpinner)
	return ok && l.d == s.d
}

func (s *DCBLeaderLineDirectionSpinner) MouseWheel(delta int) {
	// The CardinalOrdinalDirection enum goes clockwise, so adding one (mod
	// 8) goes forward, and subtracting 1 (mod 8) goes backwards.
	if delta < 0 {
		*s.d = math.CardinalOrdinalDirection((*s.d + 7) % 8)
	} else {
		*s.d = math.CardinalOrdinalDirection((*s.d + 1) % 8)
	}
}

func (s *DCBLeaderLineDirectionSpinner) KeyboardInput(text string) error {
	if len(text) > 1 {
		return ErrSTARSCommandFormat
	} else if dir, ok := numpadToDirection(text[0]); !ok || dir == nil /* entered 5 */ {
		return ErrSTARSCommandFormat
	} else {
		*s.d = *dir
		return nil
	}
}

type DCBHistoryRateSpinner struct {
	r *float32
}

func MakeHistoryRateSpinner(r *float32) DCBSpinner {
	return &DCBHistoryRateSpinner{r}
}

func (s *DCBHistoryRateSpinner) Label() string {
	return "H_RATE\n" + fmt.Sprintf("%.1f", *s.r)
}

func (s *DCBHistoryRateSpinner) Equals(other DCBSpinner) bool {
	r, ok := other.(*DCBHistoryRateSpinner)
	return ok && r.r == s.r
}

func (s *DCBHistoryRateSpinner) MouseWheel(delta int) {
	// 4-94 the spinner goes in steps of 0.5.
	if delta > 0 {
		*s.r = math.Clamp(*s.r+0.5, 0, 4.5)
	} else if delta < 0 {
		*s.r = math.Clamp(*s.r-0.5, 0, 4.5)
	}
}

func (s *DCBHistoryRateSpinner) KeyboardInput(text string) error {
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

type DCBPTLLengthSpinner struct {
	l *float32
}

func MakePTLLengthSpinner(l *float32) DCBSpinner {
	return &DCBPTLLengthSpinner{l}
}

func (s *DCBPTLLengthSpinner) Label() string {
	return "PTL\nLNTH\n" + fmt.Sprintf("%.1f", *s.l)
}

func (s *DCBPTLLengthSpinner) Equals(other DCBSpinner) bool {
	p, ok := other.(*DCBPTLLengthSpinner)
	return ok && p.l == s.l
}

func (s *DCBPTLLengthSpinner) MouseWheel(delta int) {
	// 6-16: PTLs are between 0 and 5 minutes, specified in 0.5 minute
	// increments.
	if delta > 0 {
		*s.l = math.Min(*s.l+0.5, 5)
	} else if delta < 0 {
		*s.l = math.Max(*s.l-0.5, 0)
	}
}

func (s *DCBPTLLengthSpinner) KeyboardInput(text string) error {
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

type DCBDwellModeSpinner struct {
	m *DwellMode
}

func MakeDwellModeSpinner(m *DwellMode) DCBSpinner {
	return &DCBDwellModeSpinner{m}
}

func (s *DCBDwellModeSpinner) Label() string {
	return "DWELL\n" + s.m.String()
}

func (s *DCBDwellModeSpinner) Equals(other DCBSpinner) bool {
	d, ok := other.(*DCBDwellModeSpinner)
	return ok && s.m == d.m
}

func (s *DCBDwellModeSpinner) MouseWheel(delta int) {
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

func (s *DCBDwellModeSpinner) KeyboardInput(text string) error {
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

type DCBRangeRingRadiusSpinner struct {
	r *int
}

func MakeRangeRingRadiusSpinner(radius *int) DCBSpinner {
	return &DCBRangeRingRadiusSpinner{radius}
}

func (s *DCBRangeRingRadiusSpinner) Label() string {
	return "RR\n" + strconv.Itoa(*s.r)
}

func (s *DCBRangeRingRadiusSpinner) Equals(other DCBSpinner) bool {
	r, ok := other.(*DCBRangeRingRadiusSpinner)
	return ok && r.r == s.r
}

func (s *DCBRangeRingRadiusSpinner) MouseWheel(delta int) {
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

func (s *DCBRangeRingRadiusSpinner) KeyboardInput(text string) error {
	if v, err := strconv.Atoi(text); err != nil {
		return ErrSTARSCommandFormat
	} else if v != 2 && v != 5 && v != 10 && v != 20 {
		return ErrSTARSIllegalValue
	} else {
		*s.r = v
		return nil
	}
}

// DCBBrightnessSpinner handles spinners in the BRITE menu
type DCBBrightnessSpinner struct {
	text     string
	b        *STARSBrightness
	min      STARSBrightness
	allowOff bool
}

func MakeBrightnessSpinner(t string, b *STARSBrightness, min STARSBrightness, allowOff bool) DCBSpinner {
	return &DCBBrightnessSpinner{text: t, b: b, min: min, allowOff: allowOff}
}

func (s *DCBBrightnessSpinner) Label() string {
	return s.text + " " + util.Select(*s.b == 0, "OFF", fmt.Sprintf("%2d", int(*s.b)))
}

func (s *DCBBrightnessSpinner) Equals(other DCBSpinner) bool {
	b, ok := other.(*DCBBrightnessSpinner)
	return ok && b.b == s.b
}

func (s *DCBBrightnessSpinner) MouseWheel(delta int) {
	*s.b += STARSBrightness(5 * delta)
	if *s.b < s.min && s.allowOff {
		*s.b = STARSBrightness(0)
	} else {
		*s.b = STARSBrightness(math.Clamp(*s.b, s.min, 100))
	}
}

func (s *DCBBrightnessSpinner) KeyboardInput(text string) error {
	if v, err := strconv.Atoi(text); err != nil {
		return ErrSTARSCommandFormat
	} else if v < int(s.min) || v > 100 || (v == 0 && !s.allowOff) {
		return ErrSTARSIllegalValue
	} else {
		*s.b = STARSBrightness(v)
		return nil
	}
}

func STARSSelectButton(ctx *panes.Context, text string, flags int, buttonScale float32) bool {
	_, clicked := drawDCBButton(ctx, text, flags, buttonScale, flags&buttonSelected != 0, false)
	return clicked
}

func (sp *STARSPane) STARSPlaceButton(ctx *panes.Context, text string, flags int, buttonScale float32,
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

func STARSDisabledButton(ctx *panes.Context, text string, flags int, buttonScale float32) {
	drawDCBButton(ctx, text, flags, buttonScale, false, true)
}

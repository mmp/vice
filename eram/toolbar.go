package eram

/*
TODO LIST:
	1. Fix the bug with the menus not moving after a window resize
*/

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"
)

// Find out how to get these correctly
const toolbarButtonSize = 9

const (
	toolbarMain = iota
	toolbarVideomap
	toolbarATCTools
	toolbarBright
	toolbarChecklist
	toolbarCursor
	toolbarDBFields
	toolbarFont
	toolbarViews
	toolbarDraw
	toolbarWX
	toolbarABSettings
	toolbarCommandMenu
	toolbarMapBright
	toolbarRadarFilter
	toolbarPrefSet
)

var ( // TODO: Change to actual colors, but these STARS ones will suffice for now. The colors do vary based on button so maybe
	// a seperate field in each individual button is needed?
	toolbarButtonColor            = renderer.RGB{0, 0, .867}
	toolbarTearoffButtonColor     = renderer.RGB{1, 1, .576}
	toolbarActiveButtonColor      = renderer.RGB{.906, .616, .6}
	toolbarTextColor              = renderer.RGB{.953, .953, .953}
	toolbarUnsupportedButtonColor = renderer.RGB{.4, .4, .4}
	toolbarUnsupportedTextColor   = renderer.RGB{.8, .8, .8} // Dont think I neeed this
	toolbarDisabledButtonColor    = renderer.RGB{0, .173 / 2, 0}
	toolbarDisabledTextColor      = renderer.RGB{.5, 0.5, 0.5} // Dont think I need this either
	toolbarButtonGreenColor       = renderer.RGB{0, .804, 0}
	toolbarOutlineColor           = renderer.RGB{.38, .38, .38}
	menuOutlineColor              = renderer.RGB{1, .761, 0}
	toolbarHoveredOutlineColor    = renderer.RGB{.953, .953, .953}
	eramGray                      = renderer.RGB{.78, .78, .78}
	eramDarkGray                  = renderer.RGB{.404, .404, .404}
	toolbarTearoffDisabledColor   = renderer.RGB{.7, .7, .7} // Light gray for torn-off tearoff buttons
)

type toolbarFlags int

const (
	buttonFull toolbarFlags = 1 << iota
	buttonTearoff
	buttonDisabled
	buttonUnsupported
	buttonBoth

	buttonHold
)

var menuButtons []string = []string{"DRAW", "ATC\nTOOLS", "AB\nSETTING",
	"CURSOR", "BRIGHT", "MAP BRIGHT", "FONT", "DB\nFIELDS",
	"VIEWS", "CHECK\nLISTS", "COMMAND\nMENUS", "VIDEOMAP",
	"ALT LIM", "RADAR\nFILTER", "PREFSET"}

var toolbarButtonPositions = make(map[string][2]float32)

const masterToolbarTearoffName = "__MASTER_TOOLBAR__"

func (ep *ERAMPane) drawtoolbar(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) math.Extent2D {
	paneExtent := ctx.PaneExtent
	scale := ep.toolbarButtonScale(ctx)
	ps := ep.currentPrefs()

	ep.startDrawtoolbar(ctx, scale, transforms, cb, true, true)

	defer func() {
		ep.endDrawtoolbar()

		if ps.DisplayToolbar {
			sz := buttonSize(buttonFull, scale)
			paneExtent.P1[1] -= sz[1]
		}
	}()
	if !ps.DisplayToolbar {
		return paneExtent
	}

	ep.drawToolbarMenu(ctx, scale)

	return paneExtent
}

func (ep *ERAMPane) drawToolbarMenu(ctx *panes.Context, scale float32) {
	ps := ep.currentPrefs()

	switch ep.activeToolbarMenu {
	case toolbarMain:
		toolbarDrawState.lightToolbar = [4][2]float32{}
		toolbarDrawState.customButton = make(map[string]renderer.RGB)
		toolbarDrawState.customButton["RANGE"] = renderer.RGB{0, 0, 0}
		toolbarDrawState.customButton["ALT LIM"] = renderer.RGB{0, 0, 0}
		toolbarDrawState.customButton["VECTOR"] = renderer.RGB{0, .82, 0}
		if ep.deleteTearoffMode {
			toolbarDrawState.customButton["DELETE\nTEAROFF"] = toolbarActiveButtonColor
		} else {
			toolbarDrawState.customButton["DELETE\nTEAROFF"] = renderer.RGB{0, .804, .843}
		}
		ep.drawToolbarFullButton(ctx, "DRAW", 0, scale, false, false)
		if ep.drawToolbarFullButton(ctx, "ATC\nTOOLS", 0, scale, false, false) {
			ep.activeToolbarMenu = toolbarATCTools
		}
		ep.drawToolbarFullButton(ctx, "AB\nSETTING", 0, scale, false, false)
		var val = ep.currentPrefs().Range
		var rangeStr string
		if val >= 2 {
			rangeStr = fmt.Sprintf("RANGE\n%d", int(val))
		} else {
			rangeStr = fmt.Sprintf("RANGE\n%.2f", val) // Show 2 decimals; change as you like
		}
		ep.drawToolbarFullButton(ctx, rangeStr, 0, scale, false, false)
		if ep.drawToolbarFullButton(ctx, "CURSOR", 0, scale, false, false) {
			ep.activeToolbarMenu = toolbarCursor
		}
		if ep.drawToolbarFullButton(ctx, "BRIGHT", 0, scale, false, false) { // MANDATORY
			ep.activeToolbarMenu = toolbarBright
		}
		if ep.drawToolbarFullButton(ctx, "FONT", 0, scale, false, false) {
			ep.activeToolbarMenu = toolbarFont
		}
		if ep.drawToolbarFullButton(ctx, "DB\nFIELDS", 0, scale, false, false) {
			ep.activeToolbarMenu = toolbarDBFields
		}
		if ep.drawToolbarFullButton(ctx, fmt.Sprintf("VECTOR\n%d", ep.VelocityTime), 0, scale, false, false) {
			handleMultiplicativeClick(ep, &ep.VelocityTime, 0, 8, 2)
		}
		if ep.drawToolbarFullButton(ctx, "VIEWS", 0, scale, false, true) { // MANDATORY Done
			ep.activeToolbarMenu = toolbarViews
		}
		if ep.drawToolbarFullButton(ctx, "CHECK\nLISTS", 0, scale, false, false) {
			ep.activeToolbarMenu = toolbarChecklist
		}
		ep.drawToolbarFullButton(ctx, "COMMAND\nMENUS", 0, scale, false, false)
		if ep.drawToolbarFullButton(ctx, ep.videoMapLabel, 0, scale, false, false) { // Change to ERAM adapted name MANDATORY (This will probably be the hardest)
			ep.activeToolbarMenu = toolbarVideomap
		}
		ep.drawToolbarFullButton(ctx, fmt.Sprintf("ALT LIM\n%03vB%03v", ps.altitudeFilter[0], ps.altitudeFilter[1]), 0, scale, false, false)
		if ep.drawToolbarFullButton(ctx, "RADAR\nFILTER", 0, scale, false, false) {
			ep.activeToolbarMenu = toolbarRadarFilter
		}
		ep.drawToolbarFullButton(ctx, "PREFSET", 0, scale, false, false)
		if ep.drawToolbarFullButton(ctx, "DELETE\nTEAROFF", 0, scale, ep.deleteTearoffMode, false) {
			if ep.mousePrimaryClicked(ctx.Mouse) || ep.mouseTertiaryClicked(ctx.Mouse) {
				if !ep.deleteTearoffMode {
					ep.deleteTearoffMode = true
					ep.SetTemporaryCursor("EramDeletion", -1, "")
				} else {
					ep.SetTemporaryCursor("EramInvalidSelection", .5, "EramDeletion")
				}
			}
		}
	case toolbarATCTools:
		if toolbarDrawState.lightToolbar != [4][2]float32{} {
			t := toolbarDrawState.lightToolbar
			ep.drawLightToolbar(t[0], t[1], t[2], t[3])
		}
		main := "ATC\nTOOLS"
		toolbarDrawState.customButton[main] = toolbarActiveButtonColor
		toolbarDrawState.customButton["WX"] = toolbarButtonColor
		drawButtonSamePosition(ctx, main)
		if ep.drawToolbarFullButton(ctx, main, 0, scale, true, false) {
			ep.activeToolbarMenu = toolbarMain
			resetButtonPosDefault(ctx, scale)
			delete(toolbarDrawState.customButton, main)
		}
		// Anchor the menu extent at the start of this menu's buttons (important for torn-off menus).
		p0 := toolbarDrawState.buttonCursor
		ps := ep.currentPrefs()
		if ep.drawToolbarFullButton(ctx, "CRR\nFIX", 0, scale, ps.CRR.DisplayFixes, false) {
			ps.CRR.DisplayFixes = !ps.CRR.DisplayFixes
		}
		if ep.drawToolbarFullButton(ctx, "SPEED\nADVSRY", 0, scale, false, false) {
			// CRC doesn't even simulate this...
		}
		if ep.drawToolbarFullButton(ctx, "WX", 0, scale, false, false) {
			// Opens WX menu
		}

		btnH := buttonSize(buttonFull, scale)[1]
		p2 := [2]float32{toolbarDrawState.buttonCursor[0], p0[1] - btnH}
		p1 := [2]float32{p2[0], p0[1]}
		p3 := [2]float32{p0[0], p2[1]}

		toolbarDrawState.lightToolbar = [4][2]float32{p0, p1, p2, p3}
		ep.drawMenuOutline(ctx, p0, p1, p2, p3)

	case toolbarFont:
		if toolbarDrawState.lightToolbar != [4][2]float32{} {
			t := toolbarDrawState.lightToolbar
			ep.drawLightToolbar(t[0], t[1], t[2], t[3])
		}
		main := "FONT"
		toolbarDrawState.customButton[main] = toolbarActiveButtonColor // Set the custom
		drawButtonSamePosition(ctx, main)
		if ep.drawToolbarFullButton(ctx, main, 0, scale, true, false) {
			ep.activeToolbarMenu = toolbarMain
			resetButtonPosDefault(ctx, scale)
			delete(toolbarDrawState.customButton, main)
		}

		p0 := toolbarDrawState.buttonCursor
		sz := util.Select(ps.Line4Size > 0, fmt.Sprint(ps.Line4Size), "=")
		if ep.drawToolbarFullButton(ctx, fmt.Sprintf("LINE4\n%v", sz), 0, scale, false, false) {
			handleClick(ep, &ps.Line4Size, -2, 0, 1) // Handle click for Line4 size
		}
		if ep.drawToolbarFullButton(ctx, fmt.Sprintf("FDB\n%v", ps.FDBSize), 0, scale, false, false) {
			handleClick(ep, &ps.FDBSize, 1, 5, 1) // Handle click for FDB size
		}
		sz2 := util.Select(ps.PoralSize > 0, fmt.Sprint(ps.PoralSize), "=")
		if ep.drawToolbarFullButton(ctx, fmt.Sprintf("PORTAL\n%v", sz2), 0, scale, false, false) {
			handleClick(ep, &ps.PoralSize, -2, 0, 1) // Handle click for Portal size
		}
		if ep.drawToolbarFullButton(ctx, fmt.Sprintf("TOOLBAR\n%v", ps.ToolbarSize), 0, scale, false, false) {
			handleClick(ep, &ps.ToolbarSize, 1, 2, 1) // Handle click for Toolbar size
		}
		toolbarDrawState.offsetBottom = true // Offset the next row
		if ep.drawToolbarFullButton(ctx, fmt.Sprintf("LDB\n%v", ps.RDBSize), 0, scale, false, true) {
			handleClick(ep, &ps.LDBSize, 1, 5, 1) // Handle click for RDB size
		}
		if ep.drawToolbarFullButton(ctx, fmt.Sprintf("RDB\n%v", ps.LDBSize), 0, scale, false, false) {
			handleClick(ep, &ps.RDBSize, 1, 5, 1) // Handle click for LDB size
		}

		if ep.drawToolbarFullButton(ctx, fmt.Sprintf("OUTAGE\n%v", ps.OutageSize), 0, scale, false, false) {
			handleClick(ep, &ps.OutageSize, 1, 3, 1) // Handle click for Outage size
		}
		p2 := [2]float32{toolbarDrawState.buttonCursor[0], oppositeSide(toolbarDrawState.buttonCursor, buttonSize(buttonFull, scale))[1]}
		p2[0] += buttonSize(buttonBoth, scale)[0] // Move to the right side of the button
		p1 := [2]float32{p2[0], p0[1]}
		p3 := [2]float32{p0[0], p2[1]}
		toolbarDrawState.lightToolbar = [4][2]float32{p0, p1, p2, p3}
		ep.drawMenuOutline(ctx, p0, p1, p2, p3)
	case toolbarVideomap:
		if toolbarDrawState.lightToolbar != [4][2]float32{} {
			t := toolbarDrawState.lightToolbar
			ep.drawLightToolbar(t[0], t[1], t[2], t[3])
		}
		main := ep.videoMapLabel
		toolbarDrawState.customButton[main] = toolbarActiveButtonColor // Set the custom button color for VIDEOMAP
		drawButtonSamePosition(ctx, main)
		if ep.drawToolbarFullButton(ctx, main, 0, scale, true, false) {
			ep.activeToolbarMenu = toolbarMain
			resetButtonPosDefault(ctx, scale)
			delete(toolbarDrawState.customButton, main)
		}
		ep.buttonVerticalOffset(ctx)
		toolbarDrawState.buttonCursor[1] += buttonSize(buttonFull, scale)[1] + 3
		p0 := toolbarDrawState.buttonCursor
		second := ctx.Keyboard.KeyAlt()
		for i := 0; i < 40; i++ {
			if second && i == 0 {
				i = 20
			}
			var vm radar.ERAMVideoMap
			if i < len(ep.allVideoMaps) {
				vm = ep.allVideoMaps[i]
			}
			label := vm.LabelLine1 + "\n" + vm.LabelLine2
			_, vis := ps.VideoMapVisible[combine(vm.LabelLine1, vm.LabelLine2)]
			nextRow := false
			if (i == 10 && !second) || (i == 30 && second) {
				nextRow = true
			}
			if (i == 20 && !second) || i == 42 {
				break
			}

			if ep.drawToolbarFullButton(ctx, label, 0, scale, vis, nextRow) {
				if label != "" {
					if vis {
						delete(ps.VideoMapVisible, combine(vm.LabelLine1, vm.LabelLine2))
					} else {
						ps.VideoMapVisible[combine(vm.LabelLine1, vm.LabelLine2)] = nil
					}
				}
			}
		}
		p2 := [2]float32{toolbarDrawState.buttonCursor[0], oppositeSide(toolbarDrawState.buttonCursor, buttonSize(buttonFull, scale))[1]}
		p1 := [2]float32{p2[0], p0[1]}
		p3 := [2]float32{p0[0], p2[1]}
		toolbarDrawState.lightToolbar = [4][2]float32{p0, p1, p2, p3}
		ep.drawMenuOutline(ctx, p0, p1, p2, p3)
	case toolbarBright, toolbarMapBright:
		toolbarDrawState.customButton = make(map[string]renderer.RGB)
		mapb := ep.activeToolbarMenu == toolbarMapBright
		if ep.activeToolbarMenu == toolbarBright {
			toolbarDrawState.customButton["MAP\nBRIGHT"] = toolbarButtonColor
			toolbarDrawState.customButton["CPDLC"] = toolbarButtonColor
		} else {
			toolbarDrawState.customButton["MAP\nBRIGHT"] = toolbarActiveButtonColor
		}

		ps := ep.currentPrefs()
		if toolbarDrawState.lightToolbar != [4][2]float32{} {
			t := toolbarDrawState.lightToolbar
			ep.drawLightToolbar(t[0], t[1], t[2], t[3])
		}

		drawButtonSamePosition(ctx, "BRIGHT")
		if ep.drawToolbarFullButton(ctx, "BRIGHT", 0, scale, false, false) {
			ep.activeToolbarMenu = toolbarMain
			resetButtonPosDefault(ctx, scale)
			toolbarDrawState.lightToolbar = [4][2]float32{}
		}
		p0 := toolbarDrawState.buttonCursor // For outline

		if ep.drawToolbarFullButton(ctx, "MAP\nBRIGHT", 0, scale, false, false) {
			ep.activeToolbarMenu = util.Select(ep.activeToolbarMenu == toolbarBright, toolbarMapBright, toolbarBright)
		}
		var e0 [2]float32
		if mapb { // mapbright starts drawing outline
			e0 = toolbarDrawState.buttonCursor
			// Pre-activate occlusion so underlying buttons drawn afterward ignore input inside the overlay area
			btnSz := buttonSize(buttonFull, scale)
			cols, rows := float32(10), float32(2)
			e2Approx := [2]float32{e0[0] + cols*(btnSz[0]+3), e0[1] - rows*(btnSz[1]+3)}
			e1Approx := [2]float32{e2Approx[0], e0[1]}
			e3Approx := [2]float32{e0[0], e2Approx[1]}
			toolbarDrawState.occlusionActive = true
			toolbarDrawState.occlusionExtent = math.Extent2DFromPoints([][2]float32{e0, e1Approx, e2Approx, e3Approx})
		}

		if ep.drawToolbarFullButton(ctx, "CPDLC", 0, scale, false, false) {
			// handle CPDLC
		}

		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("BCKGRD\n%d", ps.Brightness.Background), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.Background, 0, 60, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("CURSOR\n%d", ps.Brightness.Cursor), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.Cursor, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("TEXT\n%d", ps.Brightness.Text), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.Text, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("PR TGT\n%d", ps.Brightness.PRTGT), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.PRTGT, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("UNP TGT\n%d", ps.Brightness.UNPTGT), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.UNPTGT, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("PR HST\n%d", ps.Brightness.PRHST), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.PRHST, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("UNP HST\n%d", ps.Brightness.UNPHST), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.UNPHST, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("LDB\n%d", ps.Brightness.LDB), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.LDB, 0, 100, 2)
		}
		text := util.Select(ps.Brightness.SLDB > 0, fmt.Sprintf("SLDB\n+%d", ps.Brightness.SLDB), "SLDB\n=")
		if ep.drawToolbarMainButton(ctx, text, 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.SLDB, 0, 20, 1)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("WX\n%d", ps.Brightness.WX), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.WX, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("NEXRAD\n%d", ps.Brightness.NEXRAD), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.NEXRAD, 0, 100, 2)
		}
		toolbarDrawState.offsetBottom = true
		toolbarDrawState.noTearoff = true
		if ep.drawToolbarFullButton(ctx, fmt.Sprintf("BCKLGHT\n%d", ps.Brightness.Backlight), 0, scale, false, true) {
			handleClick(ep, &ps.Brightness.Backlight, 0, 100, 2)
		}
		if ep.drawToolbarFullButton(ctx, fmt.Sprintf("BUTTON\n%d", ps.Brightness.Button), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.Button, 0, 100, 2)
		}
		toolbarDrawState.noTearoff = false
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("BORDER\n%d", ps.Brightness.Border), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.Border, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("TOOLBAR\n%d", ps.Brightness.Toolbar), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.Toolbar, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("TB BRDR\n%d", ps.Brightness.TBBRDR), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.TBBRDR, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("AB BRDR\n%d", ps.Brightness.ABBRDR), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.ABBRDR, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("FDB\n%d", ps.Brightness.FDB), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.FDB, 0, 100, 2)
		}
		text = util.Select(ps.Brightness.Portal != 0, fmt.Sprintf("PORTAL\n%d", ps.Brightness.Portal), "PORTAL\n=")
		if ep.drawToolbarMainButton(ctx, text, 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.Portal, -10, 10, 1)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("SATCOMM\n%d", ps.Brightness.Satcomm), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.Satcomm, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("ON-FREQ\n%d", ps.Brightness.ONFREQ), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.ONFREQ, 0, 100, 2)
		}
		text = util.Select(ps.Brightness.Line4 > 0, fmt.Sprintf("LINE 4\n%d", ps.Brightness.Line4*-1), "LINE 4\n=")
		if ep.drawToolbarMainButton(ctx, text, 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.Line4, 0, 20, 1)
		}
		text = util.Select(ps.Brightness.Dwell > 0, fmt.Sprintf("DWELL\n+%d", ps.Brightness.Dwell), "DWELL\n=")
		if ep.drawToolbarMainButton(ctx, text, 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.Dwell, 0, 20, 1)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("FENCE\n%d", ps.Brightness.Fence), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.Fence, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("DBFEL\n%d", ps.Brightness.DBFEL), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.DBFEL, 0, 100, 2)
		}
		p2 := oppositeSide(toolbarDrawState.buttonCursor, buttonSize(buttonFull, scale))
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("OUTAGE\n%d", ps.Brightness.Outage), 0, scale, false, false) {
			handleClick(ep, &ps.Brightness.Outage, 0, 100, 2)
		}
		var e1, e2, e3 [2]float32
		if mapb {

			if toolbarDrawState.lightToolbar2 != [4][2]float32{} {
				t := toolbarDrawState.lightToolbar2
				ep.drawLightToolbar(t[0], t[1], t[2], t[3])
			}

			// Build buttons for current group's video maps without reloading the library each frame
			toolbarDrawState.buttonCursor = e0
			maps := ep.allVideoMaps
			// Overlay buttons should receive input even if occlusion is active
			toolbarDrawState.processingOcclusion = true
			for i, vm := range maps {
				label := fmt.Sprintf("%s\n%d", vm.BcgName, ps.VideoMapBrightness[vm.BcgName])
				if i == 10 {
					toolbarDrawState.buttonCursor = [2]float32{e0[0], e0[1] - buttonSize(buttonFull, scale)[1] - 2}
					// toolbarDrawState.offsetBottom = true
					// toolbarDrawState.noTearoff = true
				}
				if ep.drawToolbarMainButton(ctx, label, 0, scale, false, false) {
					brightness := ps.VideoMapBrightness[vm.BcgName]
					handleClick(ep, &brightness, 0, 100, 2)
					ps.VideoMapBrightness[vm.BcgName] = brightness
				}
				if i == 19 {
					break
				}

			}
			// Calculate e1, e2, e3 from the final button cursor position (works for any number of buttons)
			e2 = toolbarDrawState.buttonCursor
			e2[1] -= buttonSize(buttonFull, scale)[1] + 1
			e1 = [2]float32{e2[0], e0[1]}
			e3 = [2]float32{e0[0], e2[1]}
			toolbarDrawState.lightToolbar2 = [4][2]float32{e0, e1, e2, e3}
			toolbarDrawState.processingOcclusion = false
			toolbarDrawState.buttonCursor = p0
		}

		p1 := [2]float32{p2[0], p0[1]}
		p3 := [2]float32{p0[0], p2[1]}
		toolbarDrawState.lightToolbar = [4][2]float32{p0, p1, p2, p3}
		// if ep.activeToolbarMenu == toolbarMapBright {
		// 	toolbarDrawState.buttonCursor = e0
		// 	for i, vm := range ep.allVideoMaps {
		// 		label := vm.Label // unlike videomaps, dont include the id
		// 		_, vis := ps.VideoMapVisible[vm.Id]
		// 		nextRow := false
		// 		if i == 11 {
		// 			moveToolbarCursor(buttonFull, buttonSize(buttonFull, scale), ctx, true) // Move to the next row
		// 			toolbarDrawState.offsetBottom = true // Offset the next row
		// 		}
		// 		if i == 22 {
		// 			break
		// 		}
		// 		if ep.drawToolbarMainButton(ctx, label, 0, scale, vis, nextRow) {
		// 			// TODO: add brightness prefs
		// 		}

		// 	}
		// 	e2 := oppositeSide(toolbarDrawState.buttonCursor, buttonSize(buttonFull, scale))
		// 	e1 := [2]float32{e2[0], e0[1]}
		// 	e3 := [2]float32{e0[0], e2[1]}
		// 	ep.drawMenuOutline(ctx, e0, e1, e2, e3)
		// } else {
		if mapb {
			p0, p1, p2, p3 = e0, e1, e2, e3
		}
		ep.drawMenuOutline(ctx, p0, p1, p2, p3)

		// Record occlusion so underlying toolbar buttons don't eat hover/clicks
		if mapb {
			toolbarDrawState.occlusionActive = true
			// Build the occlusion extent from the MAP BRIGHT submenu quad
			oc := math.Extent2DFromPoints([][2]float32{p0, p1, p2, p3})
			toolbarDrawState.occlusionExtent = oc
		}

	case toolbarRadarFilter:
		if toolbarDrawState.lightToolbar != [4][2]float32{} {
			t := toolbarDrawState.lightToolbar
			ep.drawLightToolbar(t[0], t[1], t[2], t[3])
		}
		drawButtonSamePosition(ctx, "RADAR\nFILTER")
		if ep.drawToolbarFullButton(ctx, "RADAR\nFILTER", 0, scale, true, false) {
			ep.activeToolbarMenu = toolbarMain
			resetButtonPosDefault(ctx, scale)
		}
		ep.buttonVerticalOffset(ctx)
		toolbarDrawState.buttonCursor[1] += buttonSize(buttonFull, scale)[1] + 3
		p0 := toolbarDrawState.buttonCursor
		if ep.drawToolbarFullButton(ctx, "ALL\nLDBS", 0, scale, false, false) {
			// handle drawing all LDBS
		}

		if ep.drawToolbarFullButton(ctx, "PR\nLDB", 0, scale, false, false) {
			// handle drawing PR LDB
		}
		if ep.drawToolbarFullButton(ctx, "UNP\nLDB", 0, scale, false, false) {
			// handle drawing UNP LDB
		}
		if ep.drawToolbarFullButton(ctx, "ALL\nPRIM", 0, scale, false, false) {
			// handle drawing ALL PRIM
		}
		if ep.drawToolbarFullButton(ctx, "MODE\nMODE-C", 0, scale, false, false) {
			// handle CONFLCT ALERT
		}
		rightEdge := toolbarDrawState.buttonCursor[0] - 2 // remove the +2 padding added after last button
		p1 := [2]float32{rightEdge, p0[1]}
		//p1 = oppositeHorizontal(p1, buttonSize(buttonTearoff, scale))
		// p1 := toolbarDrawState.buttonCursor
		// toolbarDrawState.offsetBottom = true

		if ep.drawToolbarFullButton(ctx, "SELECT\nBEACON", 0, scale, false, true) {
			// handle DEPT LIST
		}

		if ep.drawToolbarFullButton(ctx, "PERM\nECHO", 0, scale, false, false) {
			// handle perm echo
		}
		if ep.drawToolbarFullButton(ctx, "STROBE\nLINES", 0, scale, false, false) {
		}
		if ep.drawToolbarFullButton(ctx, fmt.Sprintf("HISTORY\n%d", ep.HistoryLength), 0, scale, false, false) {
			handleClick(ep, &ep.HistoryLength, 0, 5, 1)
		}

		p2 := [2]float32{rightEdge, toolbarDrawState.buttonCursor[1] - buttonSize(buttonFull, scale)[1]}
		p3 := [2]float32{p0[0], p2[1]}

		toolbarDrawState.lightToolbar = [4][2]float32{p0, p1, p2, p3}
		ep.drawMenuOutline(ctx, p0, p1, p2, p3)

		toolbarDrawState.customButton[fmt.Sprintf("HISTORY\n%d", ep.HistoryLength)] = toolbarButtonGreenColor

	case toolbarViews:
		if toolbarDrawState.lightToolbar != [4][2]float32{} {
			t := toolbarDrawState.lightToolbar
			ep.drawLightToolbar(t[0], t[1], t[2], t[3])
		}
		drawButtonSamePosition(ctx, "VIEWS")
		if ep.drawToolbarFullButton(ctx, "VIEWS", 0, scale, true, false) {
			ep.activeToolbarMenu = toolbarMain
			resetButtonPosDefault(ctx, scale)
		}
		ep.buttonVerticalOffset(ctx)
		toolbarDrawState.buttonCursor[1] += buttonSize(buttonFull, scale)[1] + 3
		p0 := toolbarDrawState.buttonCursor
		if ep.drawToolbarFullButton(ctx, "ALTIM\nSET", 0, scale, false, false) {
			// handle ALTIM SET
		}

		if ep.drawToolbarFullButton(ctx, "AUTO HO\nINHIB", 0, scale, false, false) {
			// handle AUTO HO INHIB
		}
		if ep.drawToolbarFullButton(ctx, "CFR", 0, scale, false, false) {
			// handle CFR
		}
		if ep.drawToolbarFullButton(ctx, "CODE", 0, scale, false, false) {
			// handle CODE
		}
		if ep.drawToolbarFullButton(ctx, "CONFLCT\nALERT", 0, scale, false, false) {
			// handle CONFLCT ALERT
		}
		if ep.drawToolbarFullButton(ctx, "CPDLC\nADV", 0, scale, false, false) {
			// handle CPDLC ADV
		}
		if ep.drawToolbarFullButton(ctx, "CPDLC\nHIST", 0, scale, false, false) {
			// handle CPDLC HIST
		}
		if ep.drawToolbarFullButton(ctx, "CPDLC\nMSGOUT", 0, scale, false, false) {
			// handle CPDLC MSGOUT
		}
		if ep.drawToolbarFullButton(ctx, "CPDLC\nTOC SET", 0, scale, false, false) {
			// handle CPDLC TOC SET
		}
		p1 := oppositeHorizontal(toolbarDrawState.buttonCursor, buttonSize(buttonFull, scale))
		p1 = oppositeHorizontal(p1, buttonSize(buttonTearoff, scale))
		// p1 := toolbarDrawState.buttonCursor
		ps := ep.currentPrefs()
		crrActive := ps.CRR.Visible || ep.crrMenuOpen
		if ep.drawToolbarFullButton(ctx, "CRR", 0, scale, crrActive, false) {
			// Toggle visibility; when enabling, default to LIST mode and keep menu closed.
			if ps.CRR.Visible {
				ps.CRR.Visible = false
				ep.crrMenuOpen = false
			} else {
				ps.CRR.Visible = true
				ps.CRR.ListMode = true
				ep.crrMenuOpen = false
			}
		}

		// toolbarDrawState.offsetBottom = true
		if ep.drawToolbarFullButton(ctx, "DEPT\nLIST", 0, scale, false, true) {
			// handle DEPT LIST
		}

		if ep.drawToolbarFullButton(ctx, "FLIGHT\nEVENT", 0, scale, false, false) {
			// handle FLIGHT EVENT
		}
		if ep.drawToolbarFullButton(ctx, "GROUP\nSUP", 0, scale, false, false) {
			// handle GROUP SUP
		}
		if ep.drawToolbarFullButton(ctx, "HOLD\nLIST", 0, scale, false, false) {
			// handle HOLD LIST
		}
		if ep.drawToolbarFullButton(ctx, "INBND\nLIST", 0, scale, false, false) {
			// handle INBND LIST
		}
		if ep.drawToolbarFullButton(ctx, "MRP\nLIST", 0, scale, false, false) {
			// handle MRP LIST
		}
		if ep.drawToolbarFullButton(ctx, "SAA\nFILTER", 0, scale, false, false) {
			// handle SAA FILTER
		}
		if ep.drawToolbarFullButton(ctx, "UA", 0, scale, false, false) {
			// handle UA
		}
		if ep.drawToolbarFullButton(ctx, "WX\nREPORT", 0, scale, false, false) {
			// handle WX REPORT
		}
		p2 := oppositeSide(toolbarDrawState.buttonCursor, buttonSize(buttonFull, scale))
		p2[0] = p1[0]
		p3 := [2]float32{p0[0], p2[1]}
		toolbarDrawState.lightToolbar = [4][2]float32{p0, p1, p2, p3}
		ep.drawMenuOutline(ctx, p0, p1, p2, p3)
	case toolbarChecklist:
		if toolbarDrawState.lightToolbar != [4][2]float32{} {
			t := toolbarDrawState.lightToolbar
			ep.drawLightToolbar(t[0], t[1], t[2], t[3])
		}
		drawButtonSamePosition(ctx, "CHECK\nLISTS")
		if ep.drawToolbarFullButton(ctx, "CHECK\nLISTS", 0, scale, true, false) {
			ep.activeToolbarMenu = toolbarMain
			resetButtonPosDefault(ctx, scale) // Reset the button position to the default
		}
		p0 := toolbarDrawState.buttonCursor
		if ep.drawToolbarFullButton(ctx, "POS\nCHECK", 0, scale, false, false) {
			// display pos check...
		}
		p2 := oppositeSide(toolbarDrawState.buttonCursor, buttonSize(buttonFull, scale))
		p2 = oppositeHorizontal(p2, buttonSize(buttonTearoff, scale))
		if ep.drawToolbarFullButton(ctx, "EMERG\nCHECK", 0, scale, false, false) {
			// display emerg check...
		}

		p1 := [2]float32{p2[0], p0[1]}
		p3 := [2]float32{p0[0], p2[1]}
		toolbarDrawState.lightToolbar = [4][2]float32{p0, p1, p2, p3}
		ep.drawMenuOutline(ctx, p0, p1, p2, p3)
	case toolbarDBFields:
		toolbarDrawState.customButton["FDB LDR"] = renderer.RGB{0, .82, 0}
		toolbarDrawState.customButton["NONADSB"] = renderer.RGB{0, .82, 0}
		toolbarDrawState.customButton["BCAST\nFLID"] = eramGray
		toolbarDrawState.customButton["PORTAL\nFENCE"] = eramGray
		if toolbarDrawState.lightToolbar != [4][2]float32{} {
			t := toolbarDrawState.lightToolbar
			ep.drawLightToolbar(t[0], t[1], t[2], t[3])
		}
		main := "DB\nFIELDS"
		toolbarDrawState.customButton[main] = toolbarActiveButtonColor // Set the custom
		drawButtonSamePosition(ctx, main)
		if ep.drawToolbarFullButton(ctx, main, 0, scale, true, false) {
			ep.activeToolbarMenu = toolbarMain
			resetButtonPosDefault(ctx, scale)
			delete(toolbarDrawState.customButton, main)
		}

		p0 := toolbarDrawState.buttonCursor
		if ep.drawToolbarHoldButton(ctx, "NON-\nRVSM", 0, scale, false, false) {
			// handle NON-RVSM
		}
		if ep.drawToolbarHoldButton(ctx, "VRI", 0, scale, false, false) {
			// handle VRI
		}
		if ep.drawToolbarHoldButton(ctx, "CODE", 0, scale, false, false) {
			// handle CODE
		}
		if ep.drawToolbarHoldButton(ctx, "SPEED", 0, scale, false, false) {
			// handle SPEED
		}
		if ep.drawToolbarFullButton(ctx, "DEST", 0, scale, ps.Line4Type == Line4Destination, false) {
			if ps.Line4Type == Line4Destination {
				ps.Line4Type = Line4None
			} else {
				ps.Line4Type = Line4Destination
			}
		}
		if ep.drawToolbarFullButton(ctx, "TYPE", 0, scale, ps.Line4Type == Line4Type, false) {
			if ps.Line4Type == Line4Type {
				ps.Line4Type = Line4None
			} else {
				ps.Line4Type = Line4Type
			}
		}
		if ep.drawToolbarFullButton(ctx, "FDB LDR\n1", 0, scale, false, false) {
			// handle FDB LDR
		}
		if ep.drawToolbarFullButton(ctx, "BCAST\nFLID", 0, scale, false, false) {
			// handle BCAST FLD
		}
		if ep.drawToolbarFullButton(ctx, "PORTAL\nFENCE", 0, scale, false, false) {
			// handle PORTAL FENCE
		}
		toolbarDrawState.offsetBottom = true
		if ep.drawToolbarFullButton(ctx, "NON-\nADS-B", 0, scale, false, true) {
			// handle NON-ADS-B
		}
		if ep.drawToolbarFullButton(ctx, "NONADSB\n90", 0, scale, false, false) {
			// handle NONADSB 90
		}
		if ep.drawToolbarFullButton(ctx, "SAT\nCOMM", 0, scale, false, false) {
			// handle SAT COMM
		}
		if ep.drawToolbarFullButton(ctx, "TFM\nREROUTE", 0, scale, false, false) {
			// handle TFM REROUTE
		}
		if ep.drawToolbarFullButton(ctx, "CRR\nRDB", 0, scale, false, false) {
			// handle CRR RDB
		}
		if ep.drawToolbarFullButton(ctx, "STA\nRDB", 0, scale, false, false) {
			// handle STA RDB
		}
		if ep.drawToolbarFullButton(ctx, "DELAY\nRDB", 0, scale, false, false) {
			// handle DELAY RDB
		}
		if ep.drawToolbarFullButton(ctx, "DELAY\nFORMAT", 0, scale, false, false) {
			// handle DELAY FORMAT
		}
		p2 := oppositeSide(toolbarDrawState.buttonCursor, buttonSize(buttonFull, scale))
		p2 = oppositeHorizontal(p2, buttonSize(buttonTearoff, scale))
		p1 := [2]float32{p2[0], p0[1]}
		p3 := [2]float32{p0[0], p2[1]}

		toolbarDrawState.lightToolbar = [4][2]float32{p0, p1, p2, p3}
		ep.drawMenuOutline(ctx, p0, p1, p2, p3)
	case toolbarCursor:
		if toolbarDrawState.lightToolbar != [4][2]float32{} {
			t := toolbarDrawState.lightToolbar
			ep.drawLightToolbar(t[0], t[1], t[2], t[3])
		}
		toolbarDrawState.customButton["SPEED"] = toolbarButtonGreenColor
		toolbarDrawState.customButton["SIZE"] = toolbarButtonGreenColor
		toolbarDrawState.customButton["VOLUME"] = toolbarButtonGreenColor
		drawButtonSamePosition(ctx, "CURSOR")
		if ep.drawToolbarFullButton(ctx, "CURSOR", 0, scale, true, false) {
			ep.activeToolbarMenu = toolbarMain
			resetButtonPosDefault(ctx, scale)
		}
		p0 := toolbarDrawState.buttonCursor
		if ep.drawToolbarMainButton(ctx, "SPEED\n1", 0, scale, false, false) {
			// handle SPEED
		}
		label := fmt.Sprintf("SIZE\n%d", ps.CursorSize)
		if ep.drawToolbarMainButton(ctx, label, 0, scale, false, false) {
			handleClick(ep, &ps.CursorSize, 1, 5, 1) // Handle click for Cursor size
		}
		p2 := oppositeSide(toolbarDrawState.buttonCursor, buttonSize(buttonFull, scale))
		if ep.drawToolbarMainButton(ctx, "VOLUME\n5", 0, scale, false, false) {
			// handle VOLUME
		}
		p1 := [2]float32{p2[0], p0[1]}
		p3 := [2]float32{p0[0], p2[1]}
		toolbarDrawState.lightToolbar = [4][2]float32{p0, p1, p2, p3}
		ep.drawMenuOutline(ctx, p0, p1, p2, p3)

	}
}

// Set the location of the new button to the same as when it was in the main toolbar
func drawButtonSamePosition(ctx *panes.Context, text string) {
	if pos, ok := toolbarDrawState.buttonPositions[text]; ok {
		toolbarDrawState.buttonDrawStartPos = [2]float32{pos[0], ctx.PaneExtent.Height() - pos[1]}
		toolbarDrawState.buttonDrawStartPos[0] -= 10
		toolbarDrawState.buttonCursor = toolbarDrawState.buttonDrawStartPos
	}
}

func resetButtonPosDefault(ctx *panes.Context, scale float32) {
	pos := mainButtonPosition(scale)
	toolbarDrawState.buttonDrawStartPos = [2]float32{pos[0], ctx.PaneExtent.Height() - pos[1]}
}

func (ep *ERAMPane) toolbarButtonScale(ctx *panes.Context) float32 {
	// Toolbar/ buttons should be the same size no matter the window size
	return toolbarButtonSize
}

// Draws both the full button and tearoff. Only need the disabled flag. Only return the result of the full button. The tearoff will be handled here as it's all the same.
func (ep *ERAMPane) drawToolbarFullButton(ctx *panes.Context, text string, flag toolbarFlags, buttonScale float32, pushedIn, nextRow bool) bool { // Do I need to return a bool here?
	sz := buttonSize(buttonTearoff, buttonScale)
	ep.checkNextRow(nextRow, sz, ctx) // Check if we need to move to the next row

	ps := ep.currentPrefs()
	buttonName := cleanButtonName(text)
	if text == ep.videoMapLabel {
		if ps.TornOffButtons != nil {
			if pos, ok := ps.TornOffButtons[text]; ok {
				if _, exists := ps.TornOffButtons["VIDEOMAP"]; !exists {
					ps.TornOffButtons["VIDEOMAP"] = pos
				}
				delete(ps.TornOffButtons, text)
			}
		}
		buttonName = "VIDEOMAP"
	}
	toolbarDrawState.pendingTearoffName = buttonName

	// Store tearoff position for later use in tearoff click handling
	tearoffPos := toolbarDrawState.buttonCursor

	if !toolbarDrawState.noTearoff {
		ep.drawToolbarButton(ctx, "", []toolbarFlags{buttonTearoff, flag}, buttonScale, pushedIn, nextRow) // Draw tearoff button
		if nextRow {
			nextRow = false
		}
	}
	moveToolbarCursor(buttonTearoff, sz, ctx, nextRow)
	sz = buttonSize(buttonFull, buttonScale)
	pressed := ep.drawToolbarButton(ctx, text, []toolbarFlags{buttonFull, flag}, buttonScale, pushedIn, false) // Draw full button. Only change row for the tearoff button
	moveToolbarCursor(buttonFull, sz, ctx, nextRow)
	// Button spacing
	toolbarDrawState.buttonCursor[0] += 2 // Add some space between buttons

	// Handle tearoff click (only if not already tearing off something and tearoff is enabled)
	if !toolbarDrawState.noTearoff && ep.tearoffInProgress == "" {
		mouse := toolbarDrawState.mouse
		tearoffSz := buttonSize(buttonTearoff, buttonScale)
		tearoffExt := math.Extent2DFromPoints([][2]float32{
			tearoffPos,
			{tearoffPos[0] + tearoffSz[0], tearoffPos[1] - tearoffSz[1]},
		})

		if mouse != nil && tearoffExt.Inside(mouse.Pos) && ep.mousePrimaryClicked(mouse) {
			_, alreadyTorn := ps.TornOffButtons[buttonName]
			if !alreadyTorn {
				// Start new tearoff drag
				ep.tearoffInProgress = buttonName
				ep.tearoffIsReposition = false
				ep.tearoffStart = time.Now()
				ep.tearoffDragOffset = math.Sub2f(mouse.Pos, tearoffPos)
				ctx.Platform.StartCaptureMouse(ctx.PaneExtent)
			}
			// If already torn, gray tearoff is disabled - do nothing
		}
	}

	return pressed
}

// Same as above, however will only return true if constantly being held down. (For some "DB FIELDS" buttons)
func (ep *ERAMPane) drawToolbarHoldButton(ctx *panes.Context, text string, flag toolbarFlags, buttonScale float32, pushedIn, nextRow bool) bool { // Do I need to return a bool here?
	sz := buttonSize(buttonTearoff, buttonScale)
	ep.checkNextRow(nextRow, sz, ctx) // Check if we need to move to the next row

	ps := ep.currentPrefs()
	buttonName := cleanButtonName(text)
	if text == ep.videoMapLabel {
		if ps.TornOffButtons != nil {
			if pos, ok := ps.TornOffButtons[text]; ok {
				if _, exists := ps.TornOffButtons["VIDEOMAP"]; !exists {
					ps.TornOffButtons["VIDEOMAP"] = pos
				}
				delete(ps.TornOffButtons, text)
			}
		}
		buttonName = "VIDEOMAP"
	}
	toolbarDrawState.pendingTearoffName = buttonName

	// Store tearoff position for later use in tearoff click handling
	tearoffPos := toolbarDrawState.buttonCursor

	if !toolbarDrawState.noTearoff {
		ep.drawToolbarButton(ctx, "", []toolbarFlags{buttonTearoff, flag}, buttonScale, pushedIn, nextRow) // Draw tearoff button
		if nextRow {
			nextRow = false
		}
	}
	moveToolbarCursor(buttonTearoff, sz, ctx, nextRow)
	sz = buttonSize(buttonFull, buttonScale)
	pressed := ep.drawToolbarButton(ctx, text, []toolbarFlags{buttonFull, flag, buttonHold}, buttonScale, pushedIn, false) // Draw full button. Only change row for the tearoff button
	moveToolbarCursor(buttonFull, sz, ctx, nextRow)
	// Button spacing
	toolbarDrawState.buttonCursor[0] += 2 // Add some space between buttons

	// Handle tearoff click (only if not already tearing off something and tearoff is enabled)
	if !toolbarDrawState.noTearoff && ep.tearoffInProgress == "" {
		mouse := toolbarDrawState.mouse
		tearoffSz := buttonSize(buttonTearoff, buttonScale)
		tearoffExt := math.Extent2DFromPoints([][2]float32{
			tearoffPos,
			{tearoffPos[0] + tearoffSz[0], tearoffPos[1] - tearoffSz[1]},
		})

		if mouse != nil && tearoffExt.Inside(mouse.Pos) && ep.mousePrimaryClicked(mouse) {
			_, alreadyTorn := ps.TornOffButtons[buttonName]
			if !alreadyTorn {
				// Start new tearoff drag
				ep.tearoffInProgress = buttonName
				ep.tearoffIsReposition = false
				ep.tearoffStart = time.Now()
				ep.tearoffDragOffset = math.Sub2f(mouse.Pos, tearoffPos)
				ctx.Platform.StartCaptureMouse(ctx.PaneExtent)
			}
			// If already torn, gray tearoff is disabled - do nothing
		}
	}

	return pressed
}

func (ep *ERAMPane) drawToolbarMainButton(ctx *panes.Context, text string, flag toolbarFlags, buttonScale float32, pushedIn, nextRow bool) bool {
	sz := buttonSize(buttonFull, buttonScale)
	pushed := ep.drawToolbarButton(ctx, text, []toolbarFlags{buttonFull, flag}, buttonScale, pushedIn, nextRow) // Draw full button. Only change row for the tearoff button
	moveToolbarCursor(buttonFull, sz, ctx, nextRow)
	toolbarDrawState.buttonCursor[0] += 2 // Add some space between buttons; maybe we need more?
	return pushed
}

func (ep *ERAMPane) drawToolbarButton(ctx *panes.Context, text string, flags []toolbarFlags, buttonScale float32, pushedIn, nextRow bool) bool {
	ld := renderer.GetColoredLinesDrawBuilder()
	trid := renderer.GetColoredTrianglesDrawBuilder()
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	defer renderer.ReturnTextDrawBuilder(td)

	sz := buttonSize(flags[0], buttonScale)

	if toolbarDrawState.offsetBottom && (text == "" || toolbarDrawState.noTearoff) { // Only offset for the first button (the tearoff)
		ep.offsetFullButton(ctx)
		toolbarDrawState.buttonCursor[0] += 3 // Add the padding
		toolbarDrawState.offsetBottom = false
	}

	p0 := toolbarDrawState.buttonCursor
	p1 := math.Add2f(p0, [2]float32{sz[0], 0})
	p2 := math.Add2f(p1, [2]float32{0, -sz[1]})
	p3 := math.Add2f(p2, [2]float32{-sz[0], 0})

	if ep.activeToolbarMenu == toolbarMain {
		if slices.Contains(menuButtons, text) || text == ep.videoMapLabel {
			toolbarDrawState.buttonPositions[cleanButtonName(text)] = [2]float32{p0[0], ctx.PaneExtent.Height() - p0[1]}
		}
	}

	ext := math.Extent2DFromPoints([][2]float32{p0, p2})
	mouse := toolbarDrawState.mouse
	mouseInside := mouse != nil && ext.Inside(mouse.Pos)
	mouseDownInside := toolbarDrawState.mouseDownPos != nil &&
		ext.Inside([2]float32{toolbarDrawState.mouseDownPos[0], toolbarDrawState.mouseDownPos[1]}) &&
		!hasFlag(flags, buttonDisabled)

	if toolbarDrawState.occlusionActive && !toolbarDrawState.processingOcclusion {
		if mouse != nil && toolbarDrawState.occlusionExtent.Inside(mouse.Pos) {
			mouseInside = false
		}
		if toolbarDrawState.mouseDownPos != nil && toolbarDrawState.occlusionExtent.Inside([2]float32{toolbarDrawState.mouseDownPos[0], toolbarDrawState.mouseDownPos[1]}) {
			mouseDownInside = false
		}
	}

	var buttonColor, textColor renderer.RGB

	textColor = toolbarTextColor

	disabled := hasFlag(flags, buttonDisabled)
	if disabled {
		buttonColor = toolbarDisabledButtonColor
	}
	unsupported := hasFlag(flags, buttonUnsupported)
	if unsupported {
		buttonColor = toolbarUnsupportedButtonColor
	}
	if !disabled && !unsupported && hasFlag(flags, buttonFull) {
		if mouseInside && mouseDownInside {
			pushedIn = true // Maybe this needs to be toggled? TODO: Find this out.
		} else if hasFlag(flags, buttonHold) {
			pushedIn = false
		}
		if pushedIn {
			buttonColor = toolbarActiveButtonColor
			// If its a button that changes the toolbar, add it to the button position
			// With the exception of MAP BRIGHT, which opens a submenu (TODO: figure out how to do that)
		} else {
			buttonColor = toolbarButtonColor
		}

		if ep.activeToolbarMenu != toolbarMain {
			buttonColor = ep.customButtonColor(text)
			if buttonColor == (renderer.RGB{}) && pushedIn {
				buttonColor = eramGray // The black buttons turn gray when pushed
			}
		}
		if customColor, ok := toolbarDrawState.customButton[cleanButtonName(text)]; ok {
			buttonColor = customColor
			if customColor == (renderer.RGB{}) && pushedIn {
				buttonColor = eramGray // The black buttons turn gray when pushed
			}
		}

		if _, ok := toolbarDrawState.buttonPositions[cleanButtonName(text)]; !ok {
			toolbarDrawState.buttonPositions[cleanButtonName(text)] = [2]float32{p0[0], ctx.PaneExtent.Height() - p0[1]}
		}
	} else if hasFlag(flags, buttonTearoff) {
		// Check if this button has been torn off - use gray if so
		ps := ep.currentPrefs()
		buttonName := toolbarDrawState.pendingTearoffName
		if ps.TornOffButtons != nil {
			if _, tornOff := ps.TornOffButtons[buttonName]; tornOff {
				buttonColor = toolbarTearoffDisabledColor // Gray - disabled
			} else {
				buttonColor = toolbarTearoffButtonColor // Yellow - active
			}
		} else {
			buttonColor = toolbarTearoffButtonColor // Yellow - active (no tearoffs yet)
		}
	}
	ps := ep.currentPrefs()

	buttonColor = ps.Brightness.Button.ScaleRGB(buttonColor)
	textColor = ps.Brightness.Text.ScaleRGB(textColor) // Text has brightness in ERAM

	trid.AddQuad(p0, p1, p2, p3, buttonColor)
	drawToolbarText(text, td, sz, textColor)

	// Draw button outline
	outlineColor := util.Select(mouseInside, toolbarHoveredOutlineColor, toolbarOutlineColor)

	ld.AddLine(p0, p1, outlineColor)
	ld.AddLine(p1, p2, outlineColor)
	ld.AddLine(p2, p3, outlineColor)
	ld.AddLine(p3, p0, outlineColor)

	// Text last!
	trid.GenerateCommands(toolbarDrawState.cb)
	ld.GenerateCommands(toolbarDrawState.cb)
	td.GenerateCommands(toolbarDrawState.cb)

	if hasFlag(flags, buttonHold) && mouse != nil {
		if mouseInside && mouseDownInside {
			return true
		} else {
			return false
		}
	}
	if mouse != nil && mouseInside && mouseDownInside {
		now := time.Now()
		if toolbarDrawState.mouseYetReleased {
			toolbarDrawState.mouseYetReleased = false
			toolbarDrawState.lastHold = now.Add(500 * time.Millisecond)
			return true
		}
		if now.Sub(toolbarDrawState.lastHold) >= holdDuration {
			if toolbarDrawState.disableHoldRepeat {
				return false
			}
			toolbarDrawState.lastHold = now
			return true
		}
	}
	return false
}

func oppositeSide(p0, sz [2]float32) [2]float32 {
	p1 := math.Add2f(p0, [2]float32{sz[0], 0})
	return math.Add2f(p1, [2]float32{0, -sz[1]})
}

func oppositeHorizontal(p0, sz [2]float32) [2]float32 {
	return math.Add2f(p0, [2]float32{sz[0], 0})
}

func shiftLeftOne(p0, sz [2]float32) [2]float32 {
	return math.Sub2f(p0, [2]float32{sz[0], 0})
}

func hasFlag(flags []toolbarFlags, flag toolbarFlags) bool {
	return slices.Contains(flags, flag)
}

func buttonSize(flag toolbarFlags, scale float32) [2]float32 {
	bs := func(s float32) float32 { return float32(int(s*toolbarButtonSize + 0.5)) }

	// Main button length = 485u
	// Tearoff button length = 78u
	// Height = 192u
	// bs(Scale) = main button length

	// new scale: 94u tearoff length
	// newscale: 134u distance from left edge
	// ratio = 94/134 = 0.7014
	// 22u distance from the top
	// ratio = 94/22 = 4.27

	if flag == buttonFull {
		return [2]float32{bs(scale), bs(scale) / 2.52}
	} else if flag == buttonTearoff {
		return [2]float32{bs(scale) / 7.2, bs(scale) / 2.52}
	} else if flag == buttonBoth {
		return [2]float32{bs(scale) + (bs(scale) / 7.2), bs(scale) / 2.52}
	} else {
		panic(fmt.Sprintf("unhandled starsButtonFlags %d", flag))
	}
}

const holdDuration = 125 * time.Millisecond

var toolbarDrawState struct {
	cb                 *renderer.CommandBuffer
	mouse              *platform.MouseState
	mouseDownPos       []float32
	mouseYetReleased   bool // Dont allow another click until the mouse is released
	cursor             [2]float32
	drawStartPos       [2]float32
	buttonDrawStartPos [2]float32 // This is the position of the first button in the toolbar.
	// Unlike STARS, ERAM buttons don't start from the top left corner; they are offset.
	buttonCursor    [2]float32 // This is the position of the cursor in the toolbar for buttons.
	style           renderer.TextStyle
	position        int
	buttonPositions map[string][2]float32 // Button positions stored as [x, bottomOffset].
	offsetBottom    bool
	noTearoff       bool // For objects like "BUTTON" and "BCKGRD" in the brightness menu that don't have a tearoff button
	lightToolbar    [4][2]float32
	lightToolbar2   [4][2]float32
	masterToolbar   bool

	customButton map[string]renderer.RGB // Custom button colors for the toolbar

	pendingTearoffName string // Track which button the tearoff belongs to (set before drawing tearoff)

	lastHold time.Time

	// Input occlusion handling so only the topmost overlay handles hover/click
	occlusionActive     bool
	occlusionExtent     math.Extent2D
	processingOcclusion bool

	disableHoldRepeat bool
}

func init() {
	toolbarDrawState.mouseYetReleased = true
}

func (ep *ERAMPane) startDrawtoolbar(ctx *panes.Context, buttonScale float32, transforms radar.ScopeTransformations,
	cb *renderer.CommandBuffer, drawBackground bool, captureMouse bool) {

	toolbarDrawState.cb = cb
	toolbarDrawState.mouse = ctx.Mouse
	toolbarDrawState.occlusionActive = false
	toolbarDrawState.processingOcclusion = false
	if toolbarDrawState.buttonPositions == nil {
		toolbarDrawState.buttonPositions = make(map[string][2]float32)
	}

	ps := ep.currentPrefs()
	toolbarDrawState.position = 0                                                 // Always start at the top left untill custom toolbar locations are implemented
	buttonSize1 := float32(int(ep.toolbarButtonScale(ctx)*toolbarButtonSize + 1)) // Check all of these sizes
	toolbarDrawState.drawStartPos = [2]float32{0, ctx.PaneExtent.Height() - 1}
	drawEndPos := [2]float32{ctx.PaneExtent.Width(), toolbarDrawState.drawStartPos[1] - buttonSize1}

	toolbarDrawState.cursor = toolbarDrawState.drawStartPos

	toolbarDrawState.style = renderer.TextStyle{
		Font:        ep.ERAMToolbarFont(),
		Color:       toolbarTextColor,
		LineSpacing: 0,
	}

	buttonStart := mainButtonPosition(buttonScale)
	toolbarDrawState.buttonDrawStartPos = [2]float32{buttonStart[0], ctx.PaneExtent.Height() - buttonStart[1]}
	toolbarDrawState.buttonCursor = toolbarDrawState.buttonDrawStartPos

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	if drawBackground && ps.DisplayToolbar {
		trid.AddQuad(toolbarDrawState.drawStartPos, [2]float32{drawEndPos[0], toolbarDrawState.drawStartPos[1]},
			drawEndPos, [2]float32{toolbarDrawState.drawStartPos[0], drawEndPos[1]}, ps.Brightness.Toolbar.ScaleRGB(eramGray))
		trid.GenerateCommands(cb)
	}
	if captureMouse && (ep.mousePrimaryClicked(ctx.Mouse) || ep.mouseTertiaryClicked(ctx.Mouse)) {
		toolbarDrawState.mouseDownPos = ctx.Mouse.Pos[:]
	}
}

func mainButtonPosition(buttonScale float32) [2]float32 {
	return [2]float32{buttonSize(buttonTearoff, buttonScale)[0] / 0.701, buttonSize(buttonTearoff, buttonScale)[1] / 4.27}
}

func (ep *ERAMPane) endDrawtoolbar() {
	toolbarDrawState.cb.ResetState()

	if mouse := toolbarDrawState.mouse; mouse != nil { // Not sure if this is needed, but we'll find out eventually...
		if ep.mousePrimaryReleased(mouse) || ep.mouseTertiaryReleased(mouse) {
			toolbarDrawState.mouseDownPos = nil
			toolbarDrawState.mouseYetReleased = true
		}
	}
}

func drawToolbarText(text string, td *renderer.TextDrawBuilder, buttonSize [2]float32, color renderer.RGB) {
	// Clean up the text
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	style := toolbarDrawState.style
	style.Color = color // is Renderer.LerpRGB needed here? The color inputed in scaled from in DrawToolbarButton

	y0 := toolbarDrawState.buttonCursor[1] - 1
	for _, line := range lines {
		lw, lh := style.Font.BoundText(line, style.LineSpacing)
		// Try to center the text, though if it's too big to fit in the
		// button then draw it starting from the left edge of the button so
		// that the trailing characters are the ones that are lost.
		x0 := toolbarDrawState.buttonCursor[0] + max(1, (buttonSize[0]-float32(lw))/2)

		td.AddText(line, [2]float32{x0, y0}, style)
		y0 -= float32(lh) * 1.2
	}
}

var toolbarLabel = map[int][]string{
	toolbarMain:      {},
	toolbarVideomap:  {"ARTCC"},
	toolbarATCTools:  {"ATC\nTOOLS"},
	toolbarBright:    {"BRIGHT"},
	toolbarChecklist: {"CHECK\nLISTS"},
	toolbarCursor:    {"CURSOR"},
	toolbarDBFields:  {"DB FIELDS"},
	toolbarFont:      {"FONT"},
	toolbarViews:     {"VIEWS"},
	toolbarMapBright: {"BRIGHT", "MAP\nBRIGHT"},
}

var menuColor = map[int]renderer.RGB{
	toolbarVideomap:  {0, 0, 0},               // ARTCC
	toolbarATCTools:  {0, 0, 0},               // ATC TOOLS
	toolbarBright:    toolbarButtonGreenColor, // BRIGHT
	toolbarMapBright: toolbarButtonGreenColor, // MAP BRIGHT
	toolbarChecklist: {0, 0, 0},               // CHECK LISTS
	toolbarCursor:    toolbarButtonGreenColor, // CURSOR
	toolbarDBFields:  {0, 0, 0},               // DB FIELDS
	toolbarFont:      toolbarButtonGreenColor, // FONT
	toolbarViews:     {0, 0, 0},               // VIEWS
}

func (ep *ERAMPane) customButtonColor(button string) renderer.RGB {
	if button == "" {
		return toolbarTearoffButtonColor // dont change tearoff button color
	}

	labels := toolbarLabel[ep.activeToolbarMenu]
	// Check if button matches any of the labels in the current menu
	for _, label := range labels {
		if button == label {
			return toolbarActiveButtonColor
		}
	}
	// If button doesn't match any label, use the menu color
	return menuColor[ep.activeToolbarMenu]
}

func moveToolbarCursor(flag toolbarFlags, sz [2]float32, ctx *panes.Context, nextRow bool) {
	toolbarDrawState.buttonCursor[0] += sz[0] + 1 // 1 pixel padding
}
func (ep *ERAMPane) offsetFullButton(ctx *panes.Context) {
	scale := ep.toolbarButtonScale(ctx)
	moveToolbarCursor(buttonTearoff, buttonSize(buttonTearoff, scale), ctx, false)
	moveToolbarCursor(buttonTearoff, buttonSize(buttonFull, scale), ctx, false)
}

// Turns any button with dynamic fields into a main name. (eg. Range 300 -> Range)
func cleanButtonName(name string) string {
	weirdNames := []string{"RANGE", "ALT LIM", "VECTOR", "FDB LDR", "NONADSB", "SPEED", "SIZE", "VOLUME"}
	firstLine := strings.Split(name, "\n")[0]
	if slices.Contains(weirdNames, firstLine) {
		return firstLine
	}
	return name
}

func (ep *ERAMPane) buttonVerticalOffset(ctx *panes.Context) {
	toolbarDrawState.buttonCursor[1] = toolbarDrawState.buttonDrawStartPos[1]
	toolbarDrawState.buttonCursor[0] += 1
	toolbarDrawState.buttonDrawStartPos[0] = toolbarDrawState.buttonCursor[0]
}

func (ep *ERAMPane) checkNextRow(nextRow bool, sz [2]float32, ctx *panes.Context) {
	if nextRow {
		toolbarDrawState.buttonCursor[0] = toolbarDrawState.buttonDrawStartPos[0] // Reset to the start of the row
		toolbarDrawState.buttonCursor[1] -= sz[1] + 3                             // some space in between rows
	}
}

func (ep *ERAMPane) drawMenuOutline(ctx *panes.Context, p0, p1, p2, p3 [2]float32) {
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	color := ep.currentPrefs().Brightness.Border.ScaleRGB(menuOutlineColor)
	toolbarDrawState.cb.LineWidth(3, ctx.DPIScale)
	ld.AddLine(p0, p1, color)
	ld.AddLine(p1, p2, color)
	ld.AddLine(p2, p3, color)
	ld.AddLine(p3, p0, color)
	ld.GenerateCommands(toolbarDrawState.cb)
	toolbarDrawState.cb.LineWidth(1, ctx.DPIScale)
}

func (ep *ERAMPane) drawLightToolbar(p0, p1, p2, p3 [2]float32) {
	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	trid.AddQuad(p0, p1, p2, p3, eramDarkGray)
	trid.GenerateCommands(toolbarDrawState.cb)
}

// Take both ScopeBrightness and ints for font size
func handleClick[T ~int](ep *ERAMPane, pref *T, min, max, step int) {
	v := int(*pref)

	mouse := toolbarDrawState.mouse
	if mouse == nil {
		return
	}

	if ep.mousePrimaryClicked(mouse) || ep.mousePrimaryDown(mouse) { // lower value
		if v-step >= min {
			v -= step
		} else {
			ep.SetTemporaryCursor("EramInvalidSelect", 0.5, "")
		}
	} else if ep.mouseTertiaryClicked(mouse) || ep.mouseTertiaryDown(mouse) { // raise value
		if v+step <= max {
			v += step
		} else {
			ep.SetTemporaryCursor("EramInvalidEnter", 0.5, "")
		}
	}
	*pref = T(v)
}

// Just for leader lines AFAIK
func handleMultiplicativeClick(ep *ERAMPane, pref *int, min, max, step int) {
	mouse := toolbarDrawState.mouse
	if mouse == nil {
		return
	}

	value := *pref
	if ep.mousePrimaryClicked(mouse) || ep.mousePrimaryDown(mouse) { // lower value
		if value/step > min {
			if value == 1 {
				*pref = 0
			} else {
				*pref = value / step
			}
		} else {
			ep.SetTemporaryCursor("EramInvalidSelect", 0.5, "")
		}
	} else if ep.mouseTertiaryClicked(mouse) || ep.mouseTertiaryDown(mouse) { // raise value
		if value*step < max {
			if value == 0 {
				*pref = 1
			} else {
				*pref = value * step
			}
		} else {
			ep.SetTemporaryCursor("EramInvalidEnter", 0.5, "")
		}
	}
}

// This is drawn before the toolbar is drawn so it is fine to use the fields that toolbarDraw will override.
func (ep *ERAMPane) drawMasterMenu(ctx *panes.Context, cb *renderer.CommandBuffer) {
	toolbarDrawState.cb = cb
	toolbarDrawState.mouse = ctx.Mouse
	if toolbarDrawState.buttonPositions == nil {
		toolbarDrawState.buttonPositions = make(map[string][2]float32)
	}
	ps := ep.currentPrefs()
	if ps.MasterToolbarPosition == ([2]float32{}) {
		ps.MasterToolbarPosition = [2]float32{30, ctx.PaneExtent.Height() - 100}
	}
	toolbarDrawState.buttonDrawStartPos = ps.MasterToolbarPosition
	toolbarDrawState.buttonCursor = toolbarDrawState.buttonDrawStartPos
	scale := ep.toolbarButtonScale(ctx)
	toolbarDrawState.style = renderer.TextStyle{
		Font:        ep.ERAMToolbarFont(),
		Color:       toolbarTextColor,
		LineSpacing: 0,
	}
	if ep.drawFullMasterButton(ctx, "TOOLBAR", toolbarDrawState.masterToolbar, scale, 0, false) {
		toolbarDrawState.masterToolbar = !toolbarDrawState.masterToolbar
	}
	if toolbarDrawState.masterToolbar { // Draw the rest of the buttons
		if ep.drawFullMasterButton(ctx, "MASTER\nTOOLBAR", ps.DisplayToolbar, scale, 0, false) {
			ps.DisplayToolbar = !ps.DisplayToolbar
		}
		ep.drawFullMasterButton(ctx, "MCA\nTOOLBAR", false, scale, 0, false)
		ep.drawFullMasterButton(ctx, "HORIZ\nTOOLBAR", false, scale, 0, false)
		ep.drawFullMasterButton(ctx, "LEFT\nTOOLBAR", false, scale, 0, false)
		ep.drawFullMasterButton(ctx, "RIGHT\nTOOLBAR", false, scale, 0, false)
		ep.drawFullMasterButton(ctx, "MASTER\nRAISE", false, scale, 0, true)

		ep.drawFullMasterButton(ctx, "MCA\nRAISE", false, scale, 0, false)
		ep.drawFullMasterButton(ctx, "HORIZ\nRAISE", false, scale, 0, false)
		ep.drawFullMasterButton(ctx, "LEFT\nRAISE", false, scale, 0, false)
		ep.drawFullMasterButton(ctx, "RIGHT\nRAISE", false, scale, 0, false)
	}
}

func (ep *ERAMPane) drawFullMasterButton(ctx *panes.Context, text string, pushedIn bool, scale float32, flag toolbarFlags, nextRow bool) bool {
	if text == "TOOLBAR" {
		toolbarDrawState.pendingTearoffName = masterToolbarTearoffName
	} else {
		toolbarDrawState.pendingTearoffName = ""
	}
	tearoffPos := toolbarDrawState.buttonCursor
	pressedTearoff := ep.drawMasterButton(ctx, "", pushedIn, scale, []toolbarFlags{buttonTearoff, flag}, nextRow)
	if pressedTearoff && text == "TOOLBAR" && ctx.Mouse != nil && ep.tearoffInProgress == "" {
		ep.tearoffInProgress = masterToolbarTearoffName
		ep.tearoffIsReposition = true
		ep.tearoffStart = time.Now()
		ep.tearoffDragOffset = math.Sub2f(ctx.Mouse.Pos, tearoffPos)
		ctx.Platform.StartCaptureMouse(ctx.PaneExtent)
	}
	return ep.drawMasterButton(ctx, text, pushedIn, scale, []toolbarFlags{buttonFull, flag}, false)
}

func (ep *ERAMPane) drawMasterButton(ctx *panes.Context, text string, pushedIn bool, scale float32, flags []toolbarFlags, nextRow bool) bool {
	ld := renderer.GetColoredLinesDrawBuilder()
	trid := renderer.GetColoredTrianglesDrawBuilder()
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	defer renderer.ReturnTextDrawBuilder(td)

	sz := buttonSize(flags[0], scale)
	if nextRow {
		ep.checkNextRow(nextRow, sz, ctx) // Check if we need to move to the next row
		ep.offsetFullButton(ctx)
	}
	p0 := toolbarDrawState.buttonCursor
	p1 := math.Add2f(p0, [2]float32{sz[0], 0})
	p2 := math.Add2f(p1, [2]float32{0, -sz[1]})
	p3 := math.Add2f(p2, [2]float32{-sz[0], 0})

	ext := math.Extent2DFromPoints([][2]float32{p0, p2})
	mouse := toolbarDrawState.mouse
	mouseInside := mouse != nil && ext.Inside(mouse.Pos)
	mouseDownInside := toolbarDrawState.mouseDownPos != nil &&
		ext.Inside([2]float32{toolbarDrawState.mouseDownPos[0], toolbarDrawState.mouseDownPos[1]}) &&
		!hasFlag(flags, buttonDisabled)

	var buttonColor, textColor renderer.RGB
	textColor = toolbarTextColor

	disabled := hasFlag(flags, buttonDisabled)
	if disabled {
		buttonColor = toolbarDisabledButtonColor
	}
	unsupported := hasFlag(flags, buttonUnsupported)
	if unsupported {
		buttonColor = toolbarUnsupportedButtonColor
	}

	if !disabled && !unsupported && hasFlag(flags, buttonFull) {
		if mouseInside && mouseDownInside {
			pushedIn = true
		}
		if pushedIn {
			if text != "TOOLBAR" {
				buttonColor = eramGray
			} else {
				buttonColor = toolbarActiveButtonColor
			}

		} else {
			if text != "TOOLBAR" {
				buttonColor = renderer.RGB{0, 0, 0}
			} else {
				buttonColor = toolbarButtonColor
			}
		}

	} else if hasFlag(flags, buttonTearoff) {
		// Check if this button has been torn off - use gray if so
		ps := ep.currentPrefs()
		buttonName := toolbarDrawState.pendingTearoffName
		if ps.TornOffButtons != nil {
			if _, tornOff := ps.TornOffButtons[buttonName]; tornOff {
				buttonColor = toolbarTearoffDisabledColor // Gray - disabled
			} else {
				buttonColor = toolbarTearoffButtonColor // Yellow - active
			}
		} else {
			buttonColor = toolbarTearoffButtonColor // Yellow - active (no tearoffs yet)
		}
	}
	ps := ep.currentPrefs()

	buttonColor = ps.Brightness.Button.ScaleRGB(buttonColor)
	textColor = ps.Brightness.Text.ScaleRGB(textColor) // Text has brightness in ERAM

	trid.AddQuad(p0, p1, p2, p3, buttonColor)
	drawToolbarText(text, td, sz, textColor)

	outlineColor := util.Select(mouseInside, toolbarHoveredOutlineColor, toolbarOutlineColor)
	ld.AddLine(p0, p1, outlineColor)
	ld.AddLine(p1, p2, outlineColor)
	ld.AddLine(p2, p3, outlineColor)
	ld.AddLine(p3, p0, outlineColor)

	trid.GenerateCommands(toolbarDrawState.cb)
	ld.GenerateCommands(toolbarDrawState.cb)
	td.GenerateCommands(toolbarDrawState.cb)

	moveToolbarCursor(flags[0], sz, ctx, nextRow)

	if mouse != nil && mouseInside && mouseDownInside &&
		toolbarDrawState.mouseYetReleased {
		toolbarDrawState.mouseYetReleased = false
		return true
	}
	return false
}

// drawTearoffPreview draws a white outline rectangle following the mouse while tearing off a button
func (ep *ERAMPane) drawTearoffPreview(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	if ep.tearoffInProgress == "" || ctx.Mouse == nil {
		return
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.SetScissorBounds(ctx.PaneExtent, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	cb.LineWidth(1, ctx.DPIScale)

	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)

	scale := ep.toolbarButtonScale(ctx)

	// Calculate preview size (tearoff + full button)
	tearoffSz := buttonSize(buttonTearoff, scale)
	fullSz := buttonSize(buttonFull, scale)
	gap := float32(1)
	totalWidth := tearoffSz[0] + gap + fullSz[0]
	height := tearoffSz[1]

	// Calculate preview position
	previewP0 := math.Sub2f(ctx.Mouse.Pos, ep.tearoffDragOffset)
	previewP1 := math.Add2f(previewP0, [2]float32{totalWidth, 0})
	previewP2 := math.Add2f(previewP1, [2]float32{0, -height})
	previewP3 := math.Add2f(previewP0, [2]float32{0, -height})

	// Draw white outline
	color := toolbarHoveredOutlineColor
	ld.AddLine(previewP0, previewP1, color)
	ld.AddLine(previewP1, previewP2, color)
	ld.AddLine(previewP2, previewP3, color)
	ld.AddLine(previewP3, previewP0, color)

	ld.GenerateCommands(cb)
}

// handleTearoffPlacement handles mouse click to place a torn-off button
func (ep *ERAMPane) handleTearoffPlacement(ctx *panes.Context) {
	if ep.tearoffInProgress == "" {
		return
	}

	mouse := ctx.Mouse
	if mouse == nil {
		return
	}

	// Check for placement - use Released so user can drag and release to place
	shouldPlace := false
	if time.Since(ep.tearoffStart) > 100*time.Millisecond {
		if ep.mousePrimaryReleased(mouse) || ep.mouseTertiaryReleased(mouse) {
			shouldPlace = true
		}
	}

	if shouldPlace {
		ps := ep.currentPrefs()
		// Calculate position from mouse and drag offset
		position := math.Sub2f(mouse.Pos, ep.tearoffDragOffset)
		if ep.tearoffInProgress == masterToolbarTearoffName {
			ps.MasterToolbarPosition = position
		} else {
			if ps.TornOffButtons == nil {
				ps.TornOffButtons = make(map[string][2]float32)
			}
			ps.TornOffButtons[ep.tearoffInProgress] = position
		}

		ep.tearoffInProgress = ""
		ep.tearoffIsReposition = false
		ctx.Platform.EndCaptureMouse()
	}
}

// drawTornOffButtons draws all torn-off buttons at their stored positions
func (ep *ERAMPane) drawTornOffButtons(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if len(ps.TornOffButtons) == 0 {
		return
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.SetScissorBounds(ctx.PaneExtent, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	cb.LineWidth(1, ctx.DPIScale)

	scale := ep.toolbarButtonScale(ctx)
	mouse := ctx.Mouse

	for buttonName, pos := range ps.TornOffButtons {
		pressed := ep.drawSingleTornOffButton(ctx, buttonName, pos, scale, mouse, cb)
		if pressed {
			if ep.deleteTearoffMode {
				// Delete the torn-off button
				ep.deleteTornOffButton(ps, buttonName)
				ep.deleteTearoffMode = false
			} else {
				// Trigger the button action
				ep.handleTornOffButtonClick(ctx, buttonName, pos)
			}
		}
	}
}

// handleTornOffButtonsInput processes torn-off button clicks before other UI.
// This is important because torn-off UI is visually on top (drawn last) but
// many other widgets process mouse clicks during their draw calls; if they run
// first they can "steal" the click (and even move/capture the mouse).
func (ep *ERAMPane) handleTornOffButtonsInput(ctx *panes.Context) {
	ps := ep.currentPrefs()
	mouse := ctx.Mouse
	if mouse == nil || len(ps.TornOffButtons) == 0 {
		return
	}
	if !ep.mousePrimaryClicked(mouse) && !ep.mouseTertiaryClicked(mouse) {
		return
	}

	scale := ep.toolbarButtonScale(ctx)
	tearoffSz := buttonSize(buttonTearoff, scale)
	fullSz := buttonSize(buttonFull, scale)
	gap := float32(1)

	// Deterministic hit-testing in case of overlap.
	names := util.SortedMapKeys(ps.TornOffButtons)
	for _, name := range names {
		pos := ps.TornOffButtons[name]

		// Handle extents.
		handleP0 := pos
		handleP2 := math.Add2f(handleP0, [2]float32{tearoffSz[0], -tearoffSz[1]})
		handleExt := math.Extent2DFromPoints([][2]float32{handleP0, handleP2})

		// Main button extents.
		buttonP0 := math.Add2f(pos, [2]float32{tearoffSz[0] + gap, 0})
		buttonP2 := math.Add2f(buttonP0, [2]float32{fullSz[0], -fullSz[1]})
		buttonExt := math.Extent2DFromPoints([][2]float32{buttonP0, buttonP2})

		handleHovered := handleExt.Inside(mouse.Pos)
		buttonHovered := buttonExt.Inside(mouse.Pos)
		if !handleHovered && !buttonHovered {
			continue
		}

		// In delete mode, a tertiary click deletes a torn-off button.
		if ep.deleteTearoffMode {
			if ep.mouseTertiaryClicked(mouse) {
				ep.deleteTornOffButton(ps, name)
				ep.deleteTearoffMode = false
				ep.ClearTemporaryCursor() // clear the delete cursor
			}
			// Whether we deleted or not, don't let underlying UI see this click
			// since the cursor is over an overlay widget.
			ep.clearMousePrimaryConsumed(mouse)
			ep.clearMouseTertiaryConsumed(mouse)
			return
		}

		// Click on handle - start reposition.
		if handleHovered && ep.mousePrimaryClicked(mouse) && ep.tearoffInProgress == "" {
			ep.tearoffInProgress = name
			ep.tearoffIsReposition = true
			ep.tearoffStart = time.Now()
			ep.tearoffDragOffset = math.Sub2f(mouse.Pos, pos)
			ctx.Platform.StartCaptureMouse(ctx.PaneExtent)

			ep.clearMousePrimaryConsumed(mouse)
			ep.clearMouseTertiaryConsumed(mouse)
			return
		}

		// Click on main button - trigger action.
		if buttonHovered && (ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse)) {
			ep.handleTornOffButtonClick(ctx, name, pos)

			ep.clearMousePrimaryConsumed(mouse)
			ep.clearMouseTertiaryConsumed(mouse)
			return
		}

		// Cursor is over overlay, consume the click anyway.
		ep.clearMousePrimaryConsumed(mouse)
		ep.clearMouseTertiaryConsumed(mouse)
		return
	}
}

func (ep *ERAMPane) deleteTornOffButton(ps *Preferences, buttonName string) {
	delete(ps.TornOffButtons, buttonName)
	if ep.tearoffMenus != nil {
		delete(ep.tearoffMenus, buttonName)
	}
	if ep.tearoffMenuOpened != nil {
		delete(ep.tearoffMenuOpened, buttonName)
	}
	if ep.tearoffMenuLightToolbar != nil {
		delete(ep.tearoffMenuLightToolbar, buttonName)
	}
	if ep.tearoffMenuLightToolbar2 != nil {
		delete(ep.tearoffMenuLightToolbar2, buttonName)
	}
	ep.removeTearoffMenuOrder(buttonName)
}

// drawTearoffMenus draws toolbar menus anchored to torn-off buttons.
func (ep *ERAMPane) drawTearoffMenus(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	if len(ep.tearoffMenus) == 0 {
		return
	}

	ps := ep.currentPrefs()
	scale := ep.toolbarButtonScale(ctx)

	cb.SetScissorBounds(ctx.PaneExtent, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])

	menuOrder := make([]string, 0, len(ep.tearoffMenus))
	if len(ep.tearoffMenuOrder) > 0 {
		seen := make(map[string]struct{}, len(ep.tearoffMenus))
		for _, name := range ep.tearoffMenuOrder {
			if _, ok := ep.tearoffMenus[name]; ok {
				menuOrder = append(menuOrder, name)
				seen[name] = struct{}{}
			}
		}
		for _, name := range util.SortedMapKeys(ep.tearoffMenus) {
			if _, ok := seen[name]; !ok {
				menuOrder = append(menuOrder, name)
			}
		}
	} else {
		menuOrder = util.SortedMapKeys(ep.tearoffMenus)
	}

	activeMenuForInput := ""
	if ep.tearoffMenuOpened != nil {
		var recent time.Time
		for name, openedAt := range ep.tearoffMenuOpened {
			if time.Since(openedAt) < 120*time.Millisecond {
				if activeMenuForInput == "" || openedAt.After(recent) {
					activeMenuForInput = name
					recent = openedAt
				}
			}
		}
	}
	if activeMenuForInput == "" {
		if mouse := ctx.Mouse; mouse != nil {
			for i := len(menuOrder) - 1; i >= 0; i-- {
				name := menuOrder[i]
				if _, ok := ep.tearoffMenus[name]; !ok {
					continue
				}
				if ext, ok := ep.tearoffMenuExtent(name); ok && ext.Inside(mouse.Pos) {
					activeMenuForInput = name
					break
				}
			}
		}
	}

	for _, buttonName := range menuOrder {
		menuID, ok := ep.tearoffMenus[buttonName]
		if !ok {
			continue
		}
		pos, ok := ps.TornOffButtons[buttonName]
		if !ok {
			continue
		}

		savedState := toolbarDrawState
		savedMenu := ep.activeToolbarMenu

		toolbarDrawState.buttonPositions = make(map[string][2]float32)
		toolbarDrawState.customButton = make(map[string]renderer.RGB)
		toolbarDrawState.disableHoldRepeat = true
		if ep.tearoffMenuLightToolbar != nil {
			toolbarDrawState.lightToolbar = ep.tearoffMenuLightToolbar[buttonName]
		} else {
			toolbarDrawState.lightToolbar = [4][2]float32{}
		}
		if ep.tearoffMenuLightToolbar2 != nil {
			toolbarDrawState.lightToolbar2 = ep.tearoffMenuLightToolbar2[buttonName]
		} else {
			toolbarDrawState.lightToolbar2 = [4][2]float32{}
		}

		anchorName := ep.tearoffMenuAnchor(buttonName, menuID)
		ep.setTearoffMenuAnchor(ctx, anchorName, pos)

		captureMouse := buttonName == activeMenuForInput
		if ep.tearoffMenuOpened != nil {
			if openedAt, ok := ep.tearoffMenuOpened[buttonName]; ok {
				if time.Since(openedAt) < 120*time.Millisecond {
					captureMouse = false
				} else {
					delete(ep.tearoffMenuOpened, buttonName)
				}
			}
		}
		if !captureMouse {
			toolbarDrawState.mouseDownPos = nil
		}

		ep.activeToolbarMenu = menuID
		ep.startDrawtoolbar(ctx, scale, transforms, cb, false, captureMouse)
		if !captureMouse {
			toolbarDrawState.mouseDownPos = nil
		}
		ep.drawToolbarMenu(ctx, scale)
		ep.endDrawtoolbar()

		if ep.tearoffMenuLightToolbar == nil {
			ep.tearoffMenuLightToolbar = make(map[string][4][2]float32)
		}
		ep.tearoffMenuLightToolbar[buttonName] = toolbarDrawState.lightToolbar
		if ep.tearoffMenuLightToolbar2 == nil {
			ep.tearoffMenuLightToolbar2 = make(map[string][4][2]float32)
		}
		ep.tearoffMenuLightToolbar2[buttonName] = toolbarDrawState.lightToolbar2

		// Preserve click/hold state from the active tearoff menu so it doesn't re-trigger next frame.
		if captureMouse {
			savedState.mouseDownPos = toolbarDrawState.mouseDownPos
			savedState.mouseYetReleased = toolbarDrawState.mouseYetReleased
			savedState.lastHold = toolbarDrawState.lastHold
		}

		newMenuID := ep.activeToolbarMenu
		if newMenuID == toolbarMain {
			delete(ep.tearoffMenus, buttonName)
			if ep.tearoffMenuLightToolbar != nil {
				delete(ep.tearoffMenuLightToolbar, buttonName)
			}
			if ep.tearoffMenuLightToolbar2 != nil {
				delete(ep.tearoffMenuLightToolbar2, buttonName)
			}
			ep.removeTearoffMenuOrder(buttonName)
		} else {
			ep.tearoffMenus[buttonName] = newMenuID
		}

		ep.activeToolbarMenu = savedMenu
		toolbarDrawState = savedState
	}
}

// drawSingleTornOffButton draws a single torn-off button and returns true if clicked
func (ep *ERAMPane) drawSingleTornOffButton(ctx *panes.Context, name string, pos [2]float32, scale float32, mouse *platform.MouseState, cb *renderer.CommandBuffer) bool {
	ld := renderer.GetColoredLinesDrawBuilder()
	trid := renderer.GetColoredTrianglesDrawBuilder()
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	defer renderer.ReturnTextDrawBuilder(td)

	ps := ep.currentPrefs()

	// Calculate sizes
	tearoffSz := buttonSize(buttonTearoff, scale)
	fullSz := buttonSize(buttonFull, scale)
	gap := float32(1)

	// Handle (yellow tearoff area on left side for repositioning)
	handleP0 := pos
	handleP1 := math.Add2f(handleP0, [2]float32{tearoffSz[0], 0})
	handleP2 := math.Add2f(handleP1, [2]float32{0, -tearoffSz[1]})
	handleP3 := math.Add2f(handleP0, [2]float32{0, -tearoffSz[1]})

	// Main button area
	buttonP0 := math.Add2f(handleP1, [2]float32{gap, 0})
	buttonP1 := math.Add2f(buttonP0, [2]float32{fullSz[0], 0})
	buttonP2 := math.Add2f(buttonP1, [2]float32{0, -fullSz[1]})
	buttonP3 := math.Add2f(buttonP0, [2]float32{0, -fullSz[1]})

	// Check mouse interactions
	handleExt := math.Extent2DFromPoints([][2]float32{handleP0, handleP2})
	buttonExt := math.Extent2DFromPoints([][2]float32{buttonP0, buttonP2})

	handleHovered := mouse != nil && handleExt.Inside(mouse.Pos)
	buttonHovered := mouse != nil && buttonExt.Inside(mouse.Pos)

	// Handle color (yellow for repositioning)
	handleColor := toolbarTearoffButtonColor
	handleColor = ps.Brightness.Button.ScaleRGB(handleColor)
	trid.AddQuad(handleP0, handleP1, handleP2, handleP3, handleColor)

	// Main button color
	buttonColor := ep.tornOffButtonBaseColor(name)
	// Check if this button should be pushed in (active menu or toggle state)
	pushedIn := ep.isTornOffButtonActive(name)
	if pushedIn {
		buttonColor = ep.tornOffButtonActiveColor(name)
	}
	buttonColor = ps.Brightness.Button.ScaleRGB(buttonColor)
	trid.AddQuad(buttonP0, buttonP1, buttonP2, buttonP3, buttonColor)

	// Draw text on main button
	textColor := ps.Brightness.Text.ScaleRGB(toolbarTextColor)
	// Get display text for button (may have newlines)
	displayText := ep.getTornOffButtonText(name)

	// The drawToolbarText function uses toolbarDrawState.buttonCursor, so we need to temporarily set it
	savedCursor := toolbarDrawState.buttonCursor
	savedStyle := toolbarDrawState.style
	toolbarDrawState.style = renderer.TextStyle{
		Font:        ep.ERAMToolbarFont(),
		Color:       toolbarTextColor,
		LineSpacing: 0,
	}
	toolbarDrawState.buttonCursor = buttonP0
	drawToolbarText(displayText, td, fullSz, textColor)
	toolbarDrawState.buttonCursor = savedCursor
	toolbarDrawState.style = savedStyle

	// Draw outlines
	handleOutline := util.Select(handleHovered, toolbarHoveredOutlineColor, toolbarOutlineColor)
	buttonOutline := util.Select(buttonHovered, toolbarHoveredOutlineColor, toolbarOutlineColor)

	ld.AddLine(handleP0, handleP1, handleOutline)
	ld.AddLine(handleP1, handleP2, handleOutline)
	ld.AddLine(handleP2, handleP3, handleOutline)
	ld.AddLine(handleP3, handleP0, handleOutline)

	ld.AddLine(buttonP0, buttonP1, buttonOutline)
	ld.AddLine(buttonP1, buttonP2, buttonOutline)
	ld.AddLine(buttonP2, buttonP3, buttonOutline)
	ld.AddLine(buttonP3, buttonP0, buttonOutline)

	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)

	// Handle clicks
	if mouse != nil {
		if ep.deleteTearoffMode {
			if (handleHovered || buttonHovered) && ep.mouseTertiaryClicked(mouse) {
				return true
			}
			return false
		}

		// Click on handle - start reposition
		if handleHovered && ep.mousePrimaryClicked(mouse) && ep.tearoffInProgress == "" {
			ep.tearoffInProgress = name
			ep.tearoffIsReposition = true
			ep.tearoffStart = time.Now()
			ep.tearoffDragOffset = math.Sub2f(mouse.Pos, pos)
			ctx.Platform.StartCaptureMouse(ctx.PaneExtent)
		}

		// Click on main button - trigger action
		if buttonHovered && (ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse)) {
			return true
		}
	}

	return false
}

// isTornOffButtonActive returns true if the torn-off button corresponds to the active menu
func (ep *ERAMPane) isTornOffButtonActive(name string) bool {
	if name == "DELETE\nTEAROFF" || name == "DELETETEAROFF" {
		return ep.deleteTearoffMode
	}
	if ep.tearoffMenus != nil {
		if _, ok := ep.tearoffMenus[name]; ok {
			return true
		}
	}
	ps := ep.currentPrefs()
	if ps != nil {
		if ep.getTornOffButtonText(name) == "CRR\nFIX" {
			return ps.CRR.DisplayFixes
		}
		if key, ok := ep.videoMapKeyForButton(name); ok {
			if ps.VideoMapVisible != nil {
				if _, visible := ps.VideoMapVisible[key]; visible {
					return true
				}
			}
		}
	}
	return false
}

func (ep *ERAMPane) videoMapKeyForButton(name string) (string, bool) {
	display := strings.TrimSpace(ep.getTornOffButtonText(name))
	if display == "" || display == ep.videoMapLabel || name == "VIDEOMAP" {
		return "", false
	}
	for i := range ep.allVideoMaps {
		vm := ep.allVideoMaps[i]
		line1 := strings.TrimSpace(vm.LabelLine1)
		line2 := strings.TrimSpace(vm.LabelLine2)
		if line1 == "" && line2 == "" {
			continue
		}
		label := line1
		if line2 != "" {
			if label == "" {
				label = line2
			} else {
				label = label + "\n" + line2
			}
		}
		if display == label {
			key := combine(line1, line2)
			if key == "" {
				return "", false
			}
			return key, true
		}
	}
	return "", false
}

func (ep *ERAMPane) tornOffButtonActiveColor(name string) renderer.RGB {
	if ep.getTornOffButtonText(name) == "CRR\nFIX" {
		return eramGray
	}
	if _, ok := ep.videoMapKeyForButton(name); ok {
		return eramGray
	}
	return toolbarActiveButtonColor
}

func (ep *ERAMPane) tornOffButtonBaseColor(name string) renderer.RGB {
	display := ep.getTornOffButtonText(name)
	key := cleanButtonName(display)

	// Individual torn-off buttons that should be black.
	if display == "CRR\nFIX" {
		return renderer.RGB{0, 0, 0}
	}

	if key == "RANGE" || key == "ALT LIM" {
		return renderer.RGB{0, 0, 0}
	}
	if key == "VECTOR" || key == "HISTORY" || key == "FDB LDR" || key == "NONADSB" {
		return toolbarButtonGreenColor
	}
	if display == "DELETE\nTEAROFF" {
		return renderer.RGB{0, .804, .843}
	}
	if display == "HISTORY" {
		return toolbarButtonGreenColor
	}
	if display == "BCAST\nFLID" || display == "PORTAL\nFENCE" {
		return eramGray
	}
	if _, ok := ep.videoMapKeyForButton(name); ok {
		return menuColor[toolbarVideomap]
	}
	return toolbarButtonColor
}

// getTornOffButtonText returns the display text for a torn-off button
func (ep *ERAMPane) getTornOffButtonText(name string) string {
	// Some button names are stored cleaned (no newlines) but need to be displayed with newlines
	switch name {
	case "CRR FIX", "CRRFIX":
		return "CRR\nFIX"
	case "ATCTOOLS", "ATC TOOLS":
		return "ATC\nTOOLS"
	case "ABSETTING":
		return "AB\nSETTING"
	case "DBFIELDS":
		return "DB\nFIELDS"
	case "CHECKLISTS":
		return "CHECK\nLISTS"
	case "COMMANDMENUS":
		return "COMMAND\nMENUS"
	case "RADARFILTER":
		return "RADAR\nFILTER"
	case "DELETETEAROFF":
		return "DELETE\nTEAROFF"
	case "VIDEOMAP":
		return ep.videoMapLabel
	case "RANGE":
		// Special case: Range shows current value
		val := ep.currentPrefs().Range
		if val >= 2 {
			return fmt.Sprintf("RANGE\n%d", int(val))
		}
		return fmt.Sprintf("RANGE\n%.2f", val)
	case "VECTOR":
		return fmt.Sprintf("VECTOR\n%d", ep.VelocityTime)
	case "ALT LIM":
		ps := ep.currentPrefs()
		return fmt.Sprintf("ALT LIM\n%03vB%03v", ps.altitudeFilter[0], ps.altitudeFilter[1])
	default:
		return name
	}
}

func (ep *ERAMPane) setTearoffMenuAnchor(ctx *panes.Context, buttonName string, pos [2]float32) {
	if toolbarDrawState.buttonPositions == nil {
		toolbarDrawState.buttonPositions = make(map[string][2]float32)
	}

	scale := ep.toolbarButtonScale(ctx)
	tearoffSz := buttonSize(buttonTearoff, scale)
	gap := float32(1)
	mainPos := [2]float32{pos[0] + tearoffSz[0] + gap, pos[1]}
	displayText := ep.getTornOffButtonText(buttonName)
	key := cleanButtonName(displayText)
	position := [2]float32{mainPos[0], ctx.PaneExtent.Height() - mainPos[1]}
	toolbarDrawState.buttonPositions[key] = position
	if displayText != key {
		toolbarDrawState.buttonPositions[displayText] = position
	}
	if buttonName != key && buttonName != displayText {
		toolbarDrawState.buttonPositions[buttonName] = position
	}
}

func (ep *ERAMPane) tearoffMenuAnchor(buttonName string, menuID int) string {
	switch menuID {
	case toolbarMapBright:
		return "BRIGHT"
	default:
		return buttonName
	}
}

func (ep *ERAMPane) tearoffMenuExtent(buttonName string) (math.Extent2D, bool) {
	points := make([][2]float32, 0, 8)
	if ep.tearoffMenuLightToolbar != nil {
		if quad, ok := ep.tearoffMenuLightToolbar[buttonName]; ok && quad != [4][2]float32{} {
			points = append(points, quad[0], quad[1], quad[2], quad[3])
		}
	}
	if ep.tearoffMenuLightToolbar2 != nil {
		if quad, ok := ep.tearoffMenuLightToolbar2[buttonName]; ok && quad != [4][2]float32{} {
			points = append(points, quad[0], quad[1], quad[2], quad[3])
		}
	}
	if len(points) == 0 {
		return math.Extent2D{}, false
	}
	return math.Extent2DFromPoints(points), true
}

func (ep *ERAMPane) toggleTearoffMenu(buttonName string, menuID int) {
	if ep.tearoffMenus == nil {
		ep.tearoffMenus = make(map[string]int)
	}
	if _, ok := ep.tearoffMenus[buttonName]; ok {
		delete(ep.tearoffMenus, buttonName)
		if ep.tearoffMenuOpened != nil {
			delete(ep.tearoffMenuOpened, buttonName)
		}
		if ep.tearoffMenuLightToolbar != nil {
			delete(ep.tearoffMenuLightToolbar, buttonName)
		}
		if ep.tearoffMenuLightToolbar2 != nil {
			delete(ep.tearoffMenuLightToolbar2, buttonName)
		}
		ep.removeTearoffMenuOrder(buttonName)
		return
	}
	ep.tearoffMenus[buttonName] = menuID
	if ep.tearoffMenuOpened == nil {
		ep.tearoffMenuOpened = make(map[string]time.Time)
	}
	ep.tearoffMenuOpened[buttonName] = time.Now()
	ep.bumpTearoffMenuOrder(buttonName)
}

func (ep *ERAMPane) removeTearoffMenuOrder(buttonName string) {
	if len(ep.tearoffMenuOrder) == 0 {
		return
	}
	for i, name := range ep.tearoffMenuOrder {
		if name == buttonName {
			ep.tearoffMenuOrder = append(ep.tearoffMenuOrder[:i], ep.tearoffMenuOrder[i+1:]...)
			return
		}
	}
}

func (ep *ERAMPane) bumpTearoffMenuOrder(buttonName string) {
	ep.removeTearoffMenuOrder(buttonName)
	ep.tearoffMenuOrder = append(ep.tearoffMenuOrder, buttonName)
}

func (ep *ERAMPane) clearToolbarMouseDown() {
	toolbarDrawState.mouseDownPos = nil
	toolbarDrawState.mouseYetReleased = true
}

// handleTornOffButtonClick handles a click on a torn-off button's main area
func (ep *ERAMPane) handleTornOffButtonClick(ctx *panes.Context, buttonName string, pos [2]float32) {
	ps := ep.currentPrefs()
	_ = pos

	if key, ok := ep.videoMapKeyForButton(buttonName); ok {
		if ps.VideoMapVisible == nil {
			ps.VideoMapVisible = make(map[string]interface{})
		}
		if _, vis := ps.VideoMapVisible[key]; vis {
			delete(ps.VideoMapVisible, key)
		} else {
			ps.VideoMapVisible[key] = nil
		}
		return
	}

	// Normalize through display text so older saved keys like "CRR FIX" still work.
	display := ep.getTornOffButtonText(buttonName)
	switch display {
	case "DRAW":
		// Handle DRAW button
	case "ATC\nTOOLS":
		ep.clearToolbarMouseDown()
		ep.toggleTearoffMenu(buttonName, toolbarATCTools)
	case "AB\nSETTING":
		// Handle AB SETTING (options are in ERAM settings UI in ui.go)
	case "CURSOR":
		ep.clearToolbarMouseDown()
		ep.toggleTearoffMenu(buttonName, toolbarCursor)
	case "BRIGHT":
		ep.clearToolbarMouseDown()
		ep.toggleTearoffMenu(buttonName, toolbarBright)
	case "FONT":
		ep.clearToolbarMouseDown()
		ep.toggleTearoffMenu(buttonName, toolbarFont)
	case "DB\nFIELDS":
		ep.clearToolbarMouseDown()
		ep.toggleTearoffMenu(buttonName, toolbarDBFields)
	case "VECTOR":
		handleMultiplicativeClick(ep, &ep.VelocityTime, 0, 8, 2)
	case "VIEWS":
		ep.clearToolbarMouseDown()
		ep.toggleTearoffMenu(buttonName, toolbarViews)
	case "CHECK\nLISTS":
		ep.clearToolbarMouseDown()
		ep.toggleTearoffMenu(buttonName, toolbarChecklist)
	case "COMMAND\nMENUS":
		// Handle COMMAND MENUS
	case ep.videoMapLabel:
		ep.clearToolbarMouseDown()
		ep.toggleTearoffMenu(buttonName, toolbarVideomap)
	case "MAP\nBRIGHT":
		ep.clearToolbarMouseDown()
		ep.toggleTearoffMenu(buttonName, toolbarMapBright)
	case "RADAR\nFILTER":
		ep.clearToolbarMouseDown()
		ep.toggleTearoffMenu(buttonName, toolbarRadarFilter)
		// Handle RADAR FILTER
	case "PREFSET":
		// Handle PREFSET
	case "CRR\nFIX":
		ps.CRR.DisplayFixes = !ps.CRR.DisplayFixes
	case "DELETE\nTEAROFF":
		if ep.mousePrimaryClicked(ctx.Mouse) || ep.mouseTertiaryClicked(ctx.Mouse) {
			ep.deleteTearoffMode = !ep.deleteTearoffMode
		}
	}
}

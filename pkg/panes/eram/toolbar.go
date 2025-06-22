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

	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/radar"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/util"
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
)

type toolbarFlags int

const (
	buttonFull toolbarFlags = 1 << iota
	buttonTearoff
	buttonDisabled
	buttonUnsupported
)

var menuButtons []string = []string{"DRAW", "ATC\nTOOLS", "AB\nSETTING",
	"CURSOR", "BRIGHT", "MAP BRIGHT", "FONT", "DB\nFIELDS",
	"VIEWS", "CHECK\nLISTS", "COMMAND\nMENUS", "VIDEOMAP",
	"ALT LIM", "RADAR\nFILTER", "PREFSET"}

var toolbarButtonPositions = make(map[string][2]float32)

func (ep *ERAMPane) drawtoolbar(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) (paneExtent math.Extent2D) {
	// ps := ep.currentPrefs()
	scale := ep.toolbarButtonScale(ctx)

	ep.startDrawtoolbar(ctx, scale, transforms, cb)

	defer func() {
		ep.endDrawtoolbar()

		sz := buttonSize(buttonFull, scale)

		paneExtent.P1[1] -= sz[1]
	}()
	ps := ep.currentPrefs()
	if !ps.DisplayToolbar {
		return paneExtent
	}

	switch ep.activeToolbarMenu {
	case toolbarMain:
		toolbarDrawState.lightToolbar = [4][2]float32{}
		toolbarDrawState.customButton = make(map[string]renderer.RGB)
		toolbarDrawState.customButton["RANGE"] = renderer.RGB{0, 0, 0}
		toolbarDrawState.customButton["ALT LIM"] = renderer.RGB{0, 0, 0}
		toolbarDrawState.customButton["VECTOR"] = renderer.RGB{0, .82, 0}
		toolbarDrawState.customButton["DELETE\nTEAROFF"] = renderer.RGB{0, .804, .843}
		ep.drawToolbarFullButton(ctx, "DRAW", 0, scale, false, false)
		ep.drawToolbarFullButton(ctx, "ATC\nTOOLS", 0, scale, false, false)
		ep.drawToolbarFullButton(ctx, "AB\nSETTING", 0, scale, false, false)
		var val = ep.currentPrefs().Range
		var rangeStr string
		if val >= 2 {
			rangeStr = fmt.Sprintf("RANGE\n%d", int(val))
		} else {
			rangeStr = fmt.Sprintf("RANGE\n%.2f", val) // Show 2 decimals; change as you like
		}
		ep.drawToolbarFullButton(ctx, rangeStr, 0, scale, false, false)
		ep.drawToolbarFullButton(ctx, "CURSOR", 0, scale, false, false)
		if ep.drawToolbarFullButton(ctx, "BRIGHT", 0, scale, false, false) { // MANDATORY
			ep.activeToolbarMenu = toolbarBright
		}
		if ep.drawToolbarFullButton(ctx, "FONT", 0, scale, false, false) {
			ep.activeToolbarMenu = toolbarFont
		}
		if ep.drawToolbarFullButton(ctx, "DB\nFIELDS", 0, scale, false, false) {
			ep.activeToolbarMenu = toolbarDBFields
		}
		ep.drawToolbarFullButton(ctx, "VECTOR\n0", 0, scale, false, false)
		if ep.drawToolbarFullButton(ctx, "VIEWS", 0, scale, false, true) { // MANDATORY Done
			ep.activeToolbarMenu = toolbarViews
		}
		if ep.drawToolbarFullButton(ctx, "CHECK\nLISTS", 0, scale, false, false) {
			ep.activeToolbarMenu = toolbarChecklist
		}
		ep.drawToolbarFullButton(ctx, "COMMAND\nMENUS", 0, scale, false, false)
		if ep.drawToolbarFullButton(ctx, "VIDEOMAP", 0, scale, false, false) { // Change to ERAM adapted name MANDATORY (This will probably be the hardest)
			ep.activeToolbarMenu = toolbarVideomap
		}
		ep.drawToolbarFullButton(ctx, fmt.Sprintf("ALT LIM\n%03vB%03v", ps.altitudeFilter[0], ps.altitudeFilter[1]), 0, scale, false, false)
		ep.drawToolbarFullButton(ctx, "RADAR\nFILTER", 0, scale, false, false)
		ep.drawToolbarFullButton(ctx, "PREFSET", 0, scale, false, false)
		if ep.drawToolbarFullButton(ctx, "DELETE\nTEAROFF", 0, scale, false, false) {
			// ep.deleteTearoff = true
		}
	case toolbarVideomap:
		if toolbarDrawState.lightToolbar != [4][2]float32{} {
			t := toolbarDrawState.lightToolbar
			ep.drawLightToolbar(t[0], t[1], t[2], t[3])
		}
		main := "VIDEOMAP"
		toolbarDrawState.customButton[main] = toolbarActiveButtonColor // Set the custom button color for VIDEOMAP
		drawButtonSamePosition(ctx, main)
		if ep.drawToolbarFullButton(ctx, main, 0, scale, true, false) {
			ep.activeToolbarMenu = toolbarMain
			resetButtonPosDefault(ctx, scale)
			delete(toolbarDrawState.customButton, main)
		}
		toolbarDrawState.buttonCursor[1] = ep.buttonVerticalOffset(ctx)
		p0 := toolbarDrawState.buttonCursor
		for i, vm := range ep.allVideoMaps {
			label := fmt.Sprintf("%d\n%s", vm.Id, vm.Label)
			_, vis := ps.VideoMapVisible[vm.Id]
			nextRow := false
			if i == 11 {
				nextRow = true
				toolbarDrawState.offsetBottom = true // Offset the next row
			}
			if i == 22 {
				break
			}
			if ep.drawToolbarFullButton(ctx, label, 0, scale, vis, nextRow) {
				if vis {
					delete(ps.VideoMapVisible, vm.Id)
				} else {
					ps.VideoMapVisible[vm.Id] = nil
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
		if ep.activeToolbarMenu == toolbarBright {
			toolbarDrawState.customButton["MAP\nBRIGHT"] = toolbarButtonColor
			toolbarDrawState.customButton["CPDLC"] = toolbarButtonColor
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
			// ep.activeToolbarMenu = util.Select(ep.activeToolbarMenu == toolbarBright, toolbarMapBright, toolbarBright)
		}
		// e0 := toolbarDrawState.buttonCursor
		if ep.drawToolbarFullButton(ctx, "CPDLC", 0, scale, false, false) {
			// handle CPDLC
		}

		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("BCKGRD\n%d", ps.Brightness.Background), 0, scale, false, false) {
			handleClick(&ps.Brightness.Background, 0, 60, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("CURSOR\n%d", ps.Brightness.Cursor), 0, scale, false, false) {
			handleClick(&ps.Brightness.Cursor, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("TEXT\n%d", ps.Brightness.Text), 0, scale, false, false) {
			handleClick(&ps.Brightness.Text, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("PR TGT\n%d", ps.Brightness.PRTGT), 0, scale, false, false) {
			handleClick(&ps.Brightness.PRTGT, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("UNP TGT\n%d", ps.Brightness.UNPTGT), 0, scale, false, false) {
			handleClick(&ps.Brightness.UNPTGT, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("PR HST\n%d", ps.Brightness.PRHST), 0, scale, false, false) {
			handleClick(&ps.Brightness.PRHST, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("UNP HST\n%d", ps.Brightness.UNPHST), 0, scale, false, false) {
			handleClick(&ps.Brightness.UNPHST, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("LDB\n%d", ps.Brightness.LDB), 0, scale, false, false) {
			handleClick(&ps.Brightness.LDB, 0, 100, 2)
		}
		text := util.Select(ps.Brightness.SLDB > 0, fmt.Sprintf("SLDB\n+%d", ps.Brightness.SLDB), "SLDB\n=")
		if ep.drawToolbarMainButton(ctx, text, 0, scale, false, false) {
			handleClick(&ps.Brightness.SLDB, 0, 20, 1)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("WX\n%d", ps.Brightness.WX), 0, scale, false, false) {
			handleClick(&ps.Brightness.WX, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("NEXRAD\n%d", ps.Brightness.NEXRAD), 0, scale, false, false) {
			handleClick(&ps.Brightness.NEXRAD, 0, 100, 2)
		}
		toolbarDrawState.offsetBottom = true
		toolbarDrawState.noTearoff = true
		if ep.drawToolbarFullButton(ctx, fmt.Sprintf("BCKLGHT\n%d", ps.Brightness.Backlight), 0, scale, false, true) {
			handleClick(&ps.Brightness.Backlight, 0, 100, 2)
		}
		if ep.drawToolbarFullButton(ctx, fmt.Sprintf("BUTTON\n%d", ps.Brightness.Button), 0, scale, false, false) {
			handleClick(&ps.Brightness.Button, 0, 100, 2)
		}
		toolbarDrawState.noTearoff = false
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("BORDER\n%d", ps.Brightness.Border), 0, scale, false, false) {
			handleClick(&ps.Brightness.Border, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("TOOLBAR\n%d", ps.Brightness.Toolbar), 0, scale, false, false) {
			handleClick(&ps.Brightness.Toolbar, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("TB BRDR\n%d", ps.Brightness.TBBRDR), 0, scale, false, false) {
			handleClick(&ps.Brightness.TBBRDR, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("AB BRDR\n%d", ps.Brightness.ABBRDR), 0, scale, false, false) {
			handleClick(&ps.Brightness.ABBRDR, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("FDB\n%d", ps.Brightness.FDB), 0, scale, false, false) {
			handleClick(&ps.Brightness.FDB, 0, 100, 2)
		}
		text = util.Select(ps.Brightness.Portal != 0, fmt.Sprintf("PORTAL\n%d", ps.Brightness.Portal), "PORTAL\n=")
		if ep.drawToolbarMainButton(ctx, text, 0, scale, false, false) {
			handleClick(&ps.Brightness.Portal, -10, 10, 1)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("SATCOMM\n%d", ps.Brightness.Satcomm), 0, scale, false, false) {
			handleClick(&ps.Brightness.Satcomm, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("ON-FREQ\n%d", ps.Brightness.ONFREQ), 0, scale, false, false) {
			handleClick(&ps.Brightness.ONFREQ, 0, 100, 2)
		}
		text = util.Select(ps.Brightness.Line4 > 0, fmt.Sprintf("SATCOM\n%d", ps.Brightness.Line4-20), "SATCOM\n=")
		if ep.drawToolbarMainButton(ctx, text, 0, scale, false, false) {
			handleClick(&ps.Brightness.Line4, 0, 20, 1)
		}
		text = util.Select(ps.Brightness.Dwell > 0, fmt.Sprintf("DWELL\n+%d", ps.Brightness.Dwell), "DWELL\n=")
		if ep.drawToolbarMainButton(ctx, text, 0, scale, false, false) {
			handleClick(&ps.Brightness.Dwell, 0, 20, 1)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("FENCE\n%d", ps.Brightness.Fence), 0, scale, false, false) {
			handleClick(&ps.Brightness.Fence, 0, 100, 2)
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("DBFEL\n%d", ps.Brightness.DBFEL), 0, scale, false, false) {
			handleClick(&ps.Brightness.DBFEL, 0, 100, 2)
		}
		p2 := oppositeSide(toolbarDrawState.buttonCursor, buttonSize(buttonFull, scale))
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("OUTAGE\n%d", ps.Brightness.Outage), 0, scale, false, false) {
			handleClick(&ps.Brightness.Outage, 0, 100, 2)
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
		ep.drawMenuOutline(ctx, p0, p1, p2, p3)
		// }

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
		toolbarDrawState.buttonCursor[1] = ep.buttonVerticalOffset(ctx)
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
		if ep.drawToolbarFullButton(ctx, "CRR", 0, scale, false, false) {
			// handle CRR
		}

		toolbarDrawState.offsetBottom = true
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
	}

	return paneExtent
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
	return pressed
}

func (ep *ERAMPane) drawToolbarMainButton(ctx *panes.Context, text string, flag toolbarFlags, buttonScale float32, pushedIn, nextRow bool) bool {
	sz := buttonSize(buttonFull, buttonScale)
	pushed := ep.drawToolbarButton(ctx, text, []toolbarFlags{buttonFull, flag}, buttonScale, pushedIn, nextRow) // Draw full button. Only change row for the tearoff button
	moveToolbarCursor(buttonFull, sz, ctx, nextRow)
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
		toolbarDrawState.offsetBottom = false
	}

	p0 := toolbarDrawState.buttonCursor
	p1 := math.Add2f(p0, [2]float32{sz[0], 0})
	p2 := math.Add2f(p1, [2]float32{0, -sz[1]})
	p3 := math.Add2f(p2, [2]float32{-sz[0], 0})

	if ep.activeToolbarMenu == toolbarMain {
		if slices.Contains(menuButtons, text) {
			toolbarDrawState.buttonPositions[cleanButtonName(text)] = [2]float32{p0[0], ctx.PaneExtent.Height() - p0[1]}
		}
	}

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
			pushedIn = true // Maybe this needs to be toggled? TODO: Find this out.
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
		buttonColor = toolbarTearoffButtonColor
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
	if mouse != nil && mouseInside && mouseDownInside {
		now := time.Now()
		if toolbarDrawState.mouseYetReleased {
			toolbarDrawState.mouseYetReleased = false
			toolbarDrawState.lastHold = now.Add(500 * time.Millisecond)
			return true
		}
		if now.Sub(toolbarDrawState.lastHold) >= holdDuration {
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
	masterToolbar   bool

	customButton map[string]renderer.RGB // Custom button colors for the toolbar

	lastHold time.Time
}

func init() {
	toolbarDrawState.mouseYetReleased = true
}

func (ep *ERAMPane) startDrawtoolbar(ctx *panes.Context, buttonScale float32, transforms radar.ScopeTransformations,
	cb *renderer.CommandBuffer) {

	toolbarDrawState.cb = cb
	toolbarDrawState.mouse = ctx.Mouse
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
		Font:        renderer.GetDefaultFont(), // TODO: get the right font
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
	if ps.DisplayToolbar {
		trid.AddQuad(toolbarDrawState.drawStartPos, [2]float32{drawEndPos[0], toolbarDrawState.drawStartPos[1]},
			drawEndPos, [2]float32{toolbarDrawState.drawStartPos[0], drawEndPos[1]}, ps.Brightness.Toolbar.ScaleRGB(eramGray))
		trid.GenerateCommands(cb)
	}
	if ctx.Mouse != nil && (ctx.Mouse.Clicked[platform.MouseButtonPrimary] || ctx.Mouse.Clicked[platform.MouseButtonTertiary]) {
		toolbarDrawState.mouseDownPos = ctx.Mouse.Pos[:]
	}
}

func mainButtonPosition(buttonScale float32) [2]float32 {
	return [2]float32{buttonSize(buttonTearoff, buttonScale)[0] / 0.701, buttonSize(buttonTearoff, buttonScale)[1] / 4.27}
}

func (ep *ERAMPane) endDrawtoolbar() {
	toolbarDrawState.cb.ResetState()

	if mouse := toolbarDrawState.mouse; mouse != nil { // Not sure if this is needed, but we'll find out eventually...
		if mouse.Released[platform.MouseButtonPrimary] || mouse.Released[platform.MouseButtonTertiary] {
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
	_, h := style.Font.BoundText(strings.Join(lines, "\n"), toolbarDrawState.style.LineSpacing)

	slop := buttonSize[1] - float32(h) // todo: what if negative...
	y0 := toolbarDrawState.buttonCursor[1] - 1 - slop/2
	for _, line := range lines {
		lw, lh := style.Font.BoundText(line, style.LineSpacing)
		// Try to center the text, though if it's too big to fit in the
		// button then draw it starting from the left edge of the button so
		// that the trailing characters are the ones that are lost.
		x0 := toolbarDrawState.buttonCursor[0] + max(1, (buttonSize[0]-float32(lw))/2)

		td.AddText(line, [2]float32{x0, y0}, style)
		y0 -= float32(lh)
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
	weirdNames := []string{"RANGE", "ALT LIM", "VECTOR"}
	firstLine := strings.Split(name, "\n")[0]
	if slices.Contains(weirdNames, firstLine) {
		return firstLine
	}
	return name
}

func (ep *ERAMPane) buttonVerticalOffset(ctx *panes.Context) float32 {
	return ctx.PaneExtent.Height() - mainButtonPosition(ep.toolbarButtonScale(ctx))[1]
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
	toolbarDrawState.cb.LineWidth(2, ctx.DPIScale)
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
	trid.AddQuad(p0, p1, p2, p3, eramGray)
	trid.GenerateCommands(toolbarDrawState.cb)
}

func handleClick(pref *radar.ScopeBrightness, min, max, step int) {
	mouse := toolbarDrawState.mouse
	if mouse == nil {
		return
	}

	value := int(*pref)
	if mouse.Clicked[platform.MouseButtonPrimary] || mouse.Down[platform.MouseButtonPrimary] { // lower value
		if value-step >= min {
			*pref = radar.ScopeBrightness(value - step)
		} else {
			// play a sound or something
		}
	} else if mouse.Clicked[platform.MouseButtonTertiary] || mouse.Down[platform.MouseButtonTertiary] { // raise value
		if value+step <= max {
			*pref = radar.ScopeBrightness(value + step)
		} else {
			// play a sound or something
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
	toolbarDrawState.buttonDrawStartPos = [2]float32{30, ctx.PaneExtent.Height() - 100}
	toolbarDrawState.buttonCursor = toolbarDrawState.buttonDrawStartPos
	scale := ep.toolbarButtonScale(ctx)
	if ep.drawFullMasterButton(ctx, "TOOLBAR", toolbarDrawState.masterToolbar, scale, 0, false) {
		toolbarDrawState.masterToolbar = !toolbarDrawState.masterToolbar
	}
	ps := ep.currentPrefs()
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
	ep.drawMasterButton(ctx, "", pushedIn, scale, []toolbarFlags{buttonTearoff, flag}, nextRow)
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
		buttonColor = toolbarTearoffButtonColor
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

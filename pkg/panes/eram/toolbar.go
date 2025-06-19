package eram

import (
	"fmt"
	"slices"
	"strings"

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
	menuOutlineColor			 = renderer.RGB{1, .761, 0}
	toolbarHoveredOutlineColor    = renderer.RGB{.953, .953, .953}
	eramGray = renderer.RGB{.78, .78, .78}
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

var customButton map[string]renderer.RGB = map[string]renderer.RGB{
	"RANGE":           renderer.RGB{0, 0, 0},
	"ALT LIM":         renderer.RGB{0, 0, 0},
	"VECTOR":          renderer.RGB{0, .82, 0},
	"DELETE\nTEAROFF": renderer.RGB{0, .804, .843},
	"MAP\nBRIGHT":     toolbarButtonColor,
	"CPDLC":           toolbarButtonColor,
}

var toolbarButtonPositions = make(map[string][2]float32)

func (ep *ERAMPane) drawtoolbar(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) (paneExtent math.Extent2D) {
	// ps := ep.currentPrefs()
	scale := ep.toolbarButtonScale(ctx)

	ep.startDrawtoolbar(ctx, scale, transforms, cb)
	toolbarDrawState.lightToolbar = [4][2]float32{}

	defer func() {
		ep.endDrawtoolbar()

		sz := buttonSize(buttonFull, scale)

		paneExtent.P1[1] -= sz[1]
	}()

	switch ep.activeToolbarMenu {
	case toolbarMain:

		ep.drawToolbarFullButton(ctx, "DRAW", 0, scale, false, false)
		ep.drawToolbarFullButton(ctx, "ATC\nTOOLS", 0, scale, false, false)
		ep.drawToolbarFullButton(ctx, "AB\nSETTING", 0, scale, false, false)
		ep.drawToolbarFullButton(ctx, fmt.Sprintf("RANGE\n%v", ep.currentPrefs().Range), 0, scale, false, false)
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
		ep.drawToolbarFullButton(ctx, "ALT LIM\nXXXXX", 0, scale, false, false)
		ep.drawToolbarFullButton(ctx, "RADAR\nFILTER", 0, scale, false, false)
		ep.drawToolbarFullButton(ctx, "PREFSET", 0, scale, false, false)
		if ep.drawToolbarFullButton(ctx, "DELETE\nTEAROFF", 0, scale, false, false) {
			// ep.deleteTearoff = true
		}
	case toolbarBright:
		prefs := ep.currentPrefs()
		if toolbarDrawState.lightToolbar != [4][2]float32{} {
			t := toolbarDrawState.lightToolbar
			ep.drawLightToolbar(t[0], t[1], t[2], t[3])
		}
		drawButtonSamePosition("BRIGHT")
		if ep.drawToolbarFullButton(ctx, "BRIGHT", 0, scale, false, false) {
			ep.activeToolbarMenu = toolbarMain
			resetButtonPosDefault(ctx, scale)
			toolbarDrawState.lightToolbar = [4][2]float32{}
		}
		p0 := toolbarDrawState.buttonCursor // For outline
		if ep.drawToolbarFullButton(ctx, "MAP\nBRIGHT", 0, scale, false, false) {
			// handle MAP BRIGHT
		}
		if ep.drawToolbarFullButton(ctx, "CPDLC", 0, scale, false, false) {
			// handle CPDLC
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("BCKGRD\n%d", prefs.Brightness.Background), 0, scale, false, false) {
			// handle BCKGRD
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("CURSOR\n%d", prefs.Brightness.Cursor), 0, scale, false, false) {
			// handle CURSOR
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("TEXT\n%d", prefs.Brightness.Text), 0, scale, false, false) {
			// handle TEXT
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("PR TGT\n%d", prefs.Brightness.PRTGT), 0, scale, false, false) {
			// handle PR TGT
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("UNP TGT\n%d", prefs.Brightness.UNPTGT), 0, scale, false, false) {
			// handle UNP TGT
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("PR HST\n%d", prefs.Brightness.PRHST), 0, scale, false, false) {
			// handle PR HST
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("UNP HST\n%d", prefs.Brightness.UNPHST), 0, scale, false, false) {
			// handle UNP HST
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("LDB\n%d", prefs.Brightness.LDB), 0, scale, false, false) {
			// handle LDB
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("SLDB\n+%d", prefs.Brightness.SLDB), 0, scale, false, false) {
			// handle SLDB
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("WX\n%d", prefs.Brightness.WX), 0, scale, false, false) {
			// handle WX
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("NEXRAD\n%d", prefs.Brightness.NEXRAD), 0, scale, false, false) {
			// handle NEXRAD
		}
		toolbarDrawState.offsetBottom = true
		toolbarDrawState.noTearoff = true
		if ep.drawToolbarFullButton(ctx, fmt.Sprintf("BCKLGHT\n%d", prefs.Brightness.Backlight), 0, scale, false, true) {
			// handle BCKLGHT
		}

		if ep.drawToolbarFullButton(ctx, fmt.Sprintf("BUTTON\n%d", prefs.Brightness.Button), 0, scale, false, false) {
			// handle BUTTON
		}
		toolbarDrawState.noTearoff = false
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("BORDER\n%d", prefs.Brightness.Border), 0, scale, false, false) {
			// handle BORDER
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("TOOLBAR\n%d", prefs.Brightness.Toolbar), 0, scale, false, false) {
			// handle TOOLBAR
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("TB BRDR\n%d", prefs.Brightness.TBBRDR), 0, scale, false, false) {
			// handle TB BRDR
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("AB BRDR\n%d", prefs.Brightness.ABBRDR), 0, scale, false, false) {
			// handle AB BRDR
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("FDB\n%d", prefs.Brightness.FDB), 0, scale, false, false) {
			// handle FDB
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("PORTAL\n%d", prefs.Brightness.Portal), 0, scale, false, false) {
			// handle PORTAL
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("SATCOMM\n%d", prefs.Brightness.Satcomm), 0, scale, false, false) {
			// handle SATCOMM
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("ON-FREQ\n%d", prefs.Brightness.ONFREQ), 0, scale, false, false) {
			// handle ON-FREQ
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("LINE 4\n%d", prefs.Brightness.Line4), 0, scale, false, false) {
			// handle LINE 4
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("DWELL\n+%d", prefs.Brightness.Dwell), 0, scale, false, false) {
			// handle DWELL
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("FENCE\n%d", prefs.Brightness.Fence), 0, scale, false, false) {
			// handle FENCE
		}
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("DBFEL\n%d", prefs.Brightness.DBFEL), 0, scale, false, false) {
			// handle DBFEL
		}
		p2 := oppositeSide(toolbarDrawState.buttonCursor, buttonSize(buttonFull, scale))
		if ep.drawToolbarMainButton(ctx, fmt.Sprintf("OUTAGE\n%d", prefs.Brightness.Outage), 0, scale, false, false) {
			// handle OUTAGE
		}
		p1 := [2]float32{p2[0], p0[1]}
		p3 := [2]float32{p0[0], p2[1]}
		if toolbarDrawState.lightToolbar == [4][2]float32{} {
			toolbarDrawState.lightToolbar = [4][2]float32{p0, p1,p2,p3}
		}
		ep.drawMenuOutline(p0, p1, p2, p3)

	case toolbarViews:
		drawButtonSamePosition("VIEWS")
		if ep.drawToolbarFullButton(ctx, "VIEWS", 0, scale, true, false) {
			ep.activeToolbarMenu = toolbarMain
			resetButtonPosDefault(ctx, scale)
		}
		toolbarDrawState.buttonCursor[1] = ep.buttonVerticalOffset(ctx)

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
	case toolbarChecklist:
		drawButtonSamePosition("CHECK\nLISTS")
		if ep.drawToolbarFullButton(ctx, "CHECK\nLISTS", 0, scale, true, false) {
			ep.activeToolbarMenu = toolbarMain
			resetButtonPosDefault(ctx, scale) // Reset the button position to the default
		}
		if ep.drawToolbarFullButton(ctx, "POS\nCHECK", 0, scale, false, false) {
			// display pos check...
		}
		if ep.drawToolbarFullButton(ctx, "EMERG\nCHECK", 0, scale, false, false) {
			// display emerg check...
		}
	}

	return paneExtent
}

// Set the location of the new button to the same as when it was in the main toolbar
func drawButtonSamePosition(text string) {
	toolbarDrawState.buttonDrawStartPos = toolbarDrawState.buttonPositions[text]
	toolbarDrawState.buttonDrawStartPos[0] -= 10
	toolbarDrawState.buttonCursor = toolbarDrawState.buttonDrawStartPos
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
	ep.drawToolbarButton(ctx, text, []toolbarFlags{buttonFull, flag}, buttonScale, pushedIn, nextRow) // Draw full button. Only change row for the tearoff button
	moveToolbarCursor(buttonFull, sz, ctx, nextRow)
	return pushedIn
}

func (ep *ERAMPane) drawToolbarButton(ctx *panes.Context, text string, flags []toolbarFlags, buttonScale float32, pushedIn, nextRow bool) bool {
	ld := renderer.GetColoredLinesDrawBuilder()
	trid := renderer.GetColoredTrianglesDrawBuilder()
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	defer renderer.ReturnTextDrawBuilder(td)

	sz := buttonSize(flags[0], buttonScale)

	if toolbarDrawState.offsetBottom && (text == "" || toolbarDrawState.noTearoff){ // Only offset for the first button (the tearoff)
			ep.offsetFullButton(ctx)
			toolbarDrawState.offsetBottom = false
		}

	p0 := toolbarDrawState.buttonCursor
	p1 := math.Add2f(p0, [2]float32{sz[0], 0})
	p2 := math.Add2f(p1, [2]float32{0, -sz[1]})
	p3 := math.Add2f(p2, [2]float32{-sz[0], 0})

	if ep.activeToolbarMenu == toolbarMain || ep.activeToolbarMenu == toolbarVideomap {
		if slices.Contains(menuButtons, text) {
			toolbarDrawState.buttonPositions[cleanButtonName(text)] = p0 // Store the position of the button
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
		fmt.Println("disabled")
		buttonColor = toolbarDisabledButtonColor
	}
	unsupported := hasFlag(flags, buttonUnsupported)
	if unsupported {
		fmt.Println("unsupported")
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
	} else if hasFlag(flags, buttonTearoff) {
		buttonColor = toolbarTearoffButtonColor
	}
	if ep.activeToolbarMenu != toolbarMain {
		buttonColor = ep.customButtonColor(text)
	}
	if customColor, ok := customButton[cleanButtonName(text)]; ok {
		buttonColor = customColor
		if customColor == (renderer.RGB{}) && pushedIn {
			buttonColor = eramGray // The black buttons turn gray when pushed
		}
	}
	if _, ok := toolbarDrawState.buttonPositions[cleanButtonName(text)]; !ok {
		toolbarDrawState.buttonPositions[cleanButtonName(text)] = p0 // Store the position of the button
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

	if mouse != nil && mouseInside && mouseDownInside && toolbarDrawState.mouseYetReleased {
		toolbarDrawState.mouseYetReleased = false
		return true // Unlike STARS, ERAM doesn't wat for mouse release.
	}
	return false
}

func oppositeSide(p0, sz [2]float32) [2]float32{
	p1 := math.Add2f(p0, [2]float32{sz[0], 0})
	return math.Add2f(p1, [2]float32{0, -sz[1]})
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
	buttonPositions map[string][2]float32 // This is the position of each main button in the toolbar.
	offsetBottom    bool
	noTearoff       bool // For objects like "BUTTON" and "BCKGRD" in the brightness menu that don't have a tearoff button
	lightToolbar   [4][2]float32 
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
	trid.AddQuad(toolbarDrawState.drawStartPos, [2]float32{drawEndPos[0], toolbarDrawState.drawStartPos[1]},
		drawEndPos, [2]float32{toolbarDrawState.drawStartPos[0], drawEndPos[1]}, ps.Brightness.Toolbar.ScaleRGB(eramGray))
	trid.GenerateCommands(cb)

	if ctx.Mouse != nil && ctx.Mouse.Clicked[platform.MouseButtonPrimary] {
		toolbarDrawState.mouseDownPos = ctx.Mouse.Pos[:]
	}
}

func mainButtonPosition(buttonScale float32) [2]float32 {
	return [2]float32{buttonSize(buttonTearoff, buttonScale)[0] / 0.701, buttonSize(buttonTearoff, buttonScale)[1] / 4.27}
}

func (ep *ERAMPane) endDrawtoolbar() {
	toolbarDrawState.cb.ResetState()

	if mouse := toolbarDrawState.mouse; mouse != nil { // Not sure if this is needed, but we'll find out eventually...
		if mouse.Released[platform.MouseButtonPrimary] {
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

// TODO: Change these to constants
var menuColor []renderer.RGB = []renderer.RGB{
	{0, 0, 0},               // 1: ARTCC
	{0, 0, 0},               // 2: ATC TOOLS
	toolbarButtonGreenColor, // 3: BRIGHT
	{0, 0, 0},               // 4: CHECK LISTS
	toolbarButtonGreenColor, // 5: CURSOR
	{0, 0, 0},               // 6: DB FIELDS
	toolbarButtonGreenColor, // 7: FONT
	{0, 0, 0},               // 8: VIEWS
}

var toolbarLabel = map[int]string{
	toolbarVideomap:  "ARTCC",
	toolbarATCTools:  "ATC\nTOOLS",
	toolbarBright:    "BRIGHT",
	toolbarChecklist: "CHECK\nLISTS",
	toolbarCursor:    "CURSOR",
	toolbarDBFields:  "DB FIELDS",
	toolbarFont:      "FONT",
	toolbarViews:     "VIEWS",
}

func (ep *ERAMPane) customButtonColor(button string) renderer.RGB {
	if button == "" {
		return toolbarTearoffButtonColor // dont change tearoff button color
	}
	active := ep.activeToolbarMenu - 1 // offset for main
	if main := toolbarLabel[ep.activeToolbarMenu]; main != button {
		return menuColor[active]
	} else {
		return toolbarActiveButtonColor
	}
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

func (ep *ERAMPane) drawMenuOutline(p0,p1,p2,p3 [2]float32) {
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	color := ep.currentPrefs().Brightness.Border.ScaleRGB(menuOutlineColor)
	ld.AddLine(p0, p1, color)
	ld.AddLine(p1, p2, color)
	ld.AddLine(p2, p3, color)
	ld.AddLine(p3, p0, color)
	ld.GenerateCommands(toolbarDrawState.cb)
}

func (ep *ERAMPane) drawLightToolbar(p0,p1,p2,p3 [2]float32) {
	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	trid.AddQuad(p0, p1, p2, p3, eramGray)
	trid.GenerateCommands(toolbarDrawState.cb)
}
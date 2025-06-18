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
	toolbarDraw
	toolbarViews
	toolbarATCTools
	toolbarWX
	toolbarChecklist
	toolbarABSettings
	toolbarCommandMenu
	toolbarVideomap
	toolbarCursor
	toolbarBright
	toolbarMapBright
	toolbarRadarFilter
	toolbarFont
	toolbarPrefSet
	toolbarDBFields
)

var ( // TODO: Change to actual colors, but these STARS ones will suffice for now. The colors do vary based on button so maybe
	// a seperate field in each individual button is needed?
	toolbarButtonColor            = renderer.RGB{0, 0, .867} // TODO: Make a map with the buttons that have custom colors (ie. vector, delete tearoff)
	toolbarTearoffButtonColor     = renderer.RGB{1, 1, .576}
	toolbarActiveButtonColor      = renderer.RGB{.906, .616, .6}
	toolbarTextColor              = renderer.RGB{.953, .953, .953}
	toolbarUnsupportedButtonColor = renderer.RGB{.4, .4, .4}
	toolbarUnsupportedTextColor   = renderer.RGB{.8, .8, .8} // Dont think I neeed this
	toolbarDisabledButtonColor    = renderer.RGB{0, .173 / 2, 0}
	toolbarDisabledTextColor      = renderer.RGB{.5, 0.5, 0.5}  // Dont think I need this either
	toolbarOutlineColor           = renderer.RGB{.38, .38, .38} // I don't think MacOS ERAM buttons highlight when hovered yet, so I'll check later on windows.
	toolbarHoveredOutlineColor    = renderer.RGB{.953, .953, .953}
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
	"POS\nCHECK":     renderer.RGB{0, 0, 0},
	"EMERG\nCHECK":   renderer.RGB{0, 0, 0},
	"VECTOR":          renderer.RGB{0, .82, 0},
	"DELETE\nTEAROFF": renderer.RGB{0, .804, .843},
}

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

	switch ep.activeToolbarMenu {
	case toolbarMain:

		ep.drawToolbarFullButton(ctx, "DRAW", 0, scale, false, false)
		ep.drawToolbarFullButton(ctx, "ATC\nTOOLS", 0, scale, false, false)
		ep.drawToolbarFullButton(ctx, "AB\nSETTING", 0, scale, false, false)
		ep.drawToolbarFullButton(ctx, fmt.Sprintf("RANGE\n%v", ep.currentPrefs().Range), 0, scale, false, false)
		ep.drawToolbarFullButton(ctx, "CURSOR", 0, scale, false, false)
		if ep.drawToolbarFullButton(ctx, "BRIGHT", 0, scale, false, false) {
			ep.activeToolbarMenu = toolbarBright
		}
		if ep.drawToolbarFullButton(ctx, "FONT", 0, scale, false, false) {
			ep.activeToolbarMenu = toolbarFont
		}
		if ep.drawToolbarFullButton(ctx, "DB\nFIELDS", 0, scale, false, false) {
			ep.activeToolbarMenu = toolbarDBFields
		}
		ep.drawToolbarFullButton(ctx, "VECTOR\n0", 0, scale, false, false)
		if ep.drawToolbarFullButton(ctx, "VIEWS", 0, scale, false, true) {
			ep.activeToolbarMenu = toolbarViews
		}
		if ep.drawToolbarFullButton(ctx, "CHECK\nLISTS", 0, scale, false, false) {
			ep.activeToolbarMenu = toolbarChecklist
		}
		ep.drawToolbarFullButton(ctx, "COMMAND\nMENUS", 0, scale, false, false)
		if ep.drawToolbarFullButton(ctx, "VIDEOMAP", 0, scale, false, false) { // Change to ERAM adapted name
			ep.activeToolbarMenu = toolbarVideomap
		}
		ep.drawToolbarFullButton(ctx, "ALT LIM\nXXXXX", 0, scale, false, false)
		ep.drawToolbarFullButton(ctx, "RADAR\nFILTER", 0, scale, false, false)
		ep.drawToolbarFullButton(ctx, "PREFSET", 0, scale, false, false)
		if ep.drawToolbarFullButton(ctx, "DELETE\nTEAROFF", 0, scale, false, false) {
			// ep.deleteTearoff = true
		}
	case toolbarChecklist:
		drawButtonSamePosition("CHECK\nLISTS") // Draw the button in the same position as it was in the main toolbar
		if ep.drawToolbarFullButton(ctx, "CHECK\nLISTS", 0, scale, true, false) {
			ep.activeToolbarMenu = toolbarMain
			resetButtonPosDefault(ctx, scale) // Reset the button position to the default
		}
		if ep.drawToolbarFullButton(ctx, "POS\nCHECK", 0, scale, false, false ) {
			// display pos check...
		}
		if ep.drawToolbarFullButton(ctx, "EMERG\nCHECK", 0, scale, false, false) {
			// display emerg check...
		}
	}

	return paneExtent
}

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
	ep.drawToolbarButton(ctx, "", []toolbarFlags{buttonTearoff, flag}, buttonScale, pushedIn, nextRow)     // Draw tearoff button
	return ep.drawToolbarButton(ctx, text, []toolbarFlags{buttonFull, flag}, buttonScale, pushedIn, false) // Draw full button. Only change row for the tearoff button
}

func (ep *ERAMPane) drawToolbarButton(ctx *panes.Context, text string, flags []toolbarFlags, buttonScale float32, pushedIn, nextRow bool) bool {
	ld := renderer.GetColoredLinesDrawBuilder()
	trid := renderer.GetColoredTrianglesDrawBuilder()
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	defer renderer.ReturnTextDrawBuilder(td)

	sz := buttonSize(flags[0], buttonScale)

	if nextRow {
		toolbarDrawState.buttonCursor[0] = toolbarDrawState.buttonDrawStartPos[0] // Reset to the start of the row
		toolbarDrawState.buttonCursor[1] -= sz[1] + 3                             // some space in between rows
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
	if customColor, ok := customButton[cleanButtonName(text)]; ok {
		buttonColor = customColor
		if customColor == (renderer.RGB{}) && pushedIn{
			buttonColor = renderer.RGB{.78, .78, .78} // The black buttons turn gray when pushed 
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
	moveToolbarCursor(flags[0], sz, ctx, nextRow)

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
	mouseYetReleased bool // Dont allow another click until the mouse is released
	cursor             [2]float32
	drawStartPos       [2]float32
	buttonDrawStartPos [2]float32 // This is the position of the first button in the toolbar.
	// Unlike STARS, ERAM buttons don't start from the top left corner; they are offset.
	buttonCursor    [2]float32 // This is the position of the cursor in the toolbar for buttons.
	style           renderer.TextStyle
	position        int
	buttonPositions map[string][2]float32 // This is the position of each main button in the toolbar.
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
	toolbarDrawState.position = 0                                                   // Always start at the top left untill custom toolbar locations are implemented
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
		drawEndPos, [2]float32{toolbarDrawState.drawStartPos[0], drawEndPos[1]}, ps.Brightness.Toolbar.ScaleRGB(renderer.RGB{.78, .78, .78}))
	trid.GenerateCommands(cb)

	if ctx.Mouse != nil && ctx.Mouse.Clicked[platform.MouseButtonPrimary]  {
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

func moveToolbarCursor(flag toolbarFlags, sz [2]float32, ctx *panes.Context, nextRow bool) {
	toolbarDrawState.buttonCursor[0] += sz[0] + 1 // 1 pixel padding
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

package eram

import (
	"fmt"
	"strings"

	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/radar"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/util"
)

// Find out how to get these correctly
const toolbarButtonSize = 84
const numToolbarSlots = 17

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
	toolbarButtonColor            = renderer.RGB{0, .173, 0}
	toolbarActiveButtonColor      = renderer.RGB{.431, .341, .302}
	toolbarTextColor              = renderer.RGB{1, 1, 1}
	toolbarUnsupportedButtonColor = renderer.RGB{.4, .4, .4}
	toolbarUnsupportedTextColor   = renderer.RGB{.8, .8, .8}
	toolbarDisabledButtonColor    = renderer.RGB{0, .173 / 2, 0}
	toolbarDisabledTextColor      = renderer.RGB{.5, 0.5, 0.5}
	toolbarOutlineColor           = renderer.RGB{0, 0, 0}
	toolbarHoveredOutlineColor    = renderer.RGB{.38, .38, .38}
)

type toolbarFlags int

const (
	buttonFull toolbarFlags = 1 << iota
	buttonHalfVertical
	buttonHalfHorizontal
	buttonSelected
	buttonWXAVL
	buttonDisabled
	buttonUnsupported
)

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

var activeSpinner dcbSpinner

func (ep *ERAMPane) drawtoolbar(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	// ps := ep.currentPrefs()

	scale := ep.toolbarButtonScale(ctx)

	ep.startDrawtoolbar(ctx, scale, transforms, cb)
	switch ep.activeToolbarMenu {
	case toolbarMain:

	}

}

func (ep *ERAMPane) toolbarButtonScale(ctx *panes.Context) float32 {
	ds := ctx.DrawPixelScale
	return min(ds, (ds*ctx.PaneExtent.Width()-4)/(numToolbarSlots*toolbarButtonSize))
}

func (ep *ERAMPane) drawToolbarMainButton(ctx *panes.Context, text string, flags toolbarFlags, buttonScale float32, pushedIn bool) bool {
	ld := renderer.GetColoredLinesDrawBuilder()
	trid := renderer.GetColoredTrianglesDrawBuilder()
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	defer renderer.ReturnTextDrawBuilder(td)

	sz := buttonSize(flags, buttonScale)

	p0 := toolbarDrawState.cursor
	p1 := math.Add2f(p0, [2]float32{sz[0], 0})
	p2 := math.Add2f(p1, [2]float32{0, -sz[1]})
	p3 := math.Add2f(p2, [2]float32{-sz[0], 0})

	ext := math.Extent2DFromPoints([][2]float32{p0, p2})
	mouse := toolbarDrawState.mouse
	mouseInside := mouse != nil && ext.Inside(mouse.Pos)
	mouseDownInside := toolbarDrawState.mouseDownPos != nil &&
		ext.Inside([2]float32{toolbarDrawState.mouseDownPos[0], toolbarDrawState.mouseDownPos[1]}) &&
		flags&buttonDisabled == 0

	var buttonColor, textColor renderer.RGB

	textColor = toolbarTextColor

	disabled := flags&buttonDisabled != 0
	if disabled {
		buttonColor = toolbarDisabledButtonColor
	}
	unsupported := flags&buttonUnsupported != 0
	if unsupported {
		buttonColor = toolbarUnsupportedButtonColor
	}
	if !disabled && !unsupported {
		if mouseInside && mouseDownInside {
			pushedIn = !pushedIn
		}
		  if pushedIn {
			   buttonColor = toolbarActiveButtonColor
		  } else {
			 buttonColor = toolbarButtonColor
		  }   
	}
	buttonColor = toolbarDrawState.brightness.ScaleRGB(buttonColor)
	//textColor = toolbarDrawState.brightness.ScaleRGB(textColor)

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

	if mouse != nil && mouseInside && mouse.Released[platform.MouseButtonPrimary] && mouseDownInside {
		return true /* clicked and released */
	}
	return false
}

func buttonSize(flags toolbarFlags, scale float32) [2]float32 {
	bs := func(s float32) float32 { return float32(int(s*toolbarButtonSize + 0.5)) }

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

var toolbarDrawState struct {
	cb           *renderer.CommandBuffer
	mouse        *platform.MouseState
	mouseDownPos []float32
	cursor       [2]float32
	drawStartPos [2]float32
	style        renderer.TextStyle
	brightness   radar.ScopeBrightness
	position     int
}

func (ep *ERAMPane) startDrawtoolbar(ctx *panes.Context, buttonScale float32, transforms radar.ScopeTransformations,
	cb *renderer.CommandBuffer) {
	toolbarDrawState.cb = cb
	toolbarDrawState.mouse = ctx.Mouse

	ps := ep.currentPrefs()
	toolbarDrawState.brightness = ps.Brightness.Toolbar
	toolbarDrawState.position = 0 // Always start at the top left untill custom toolbar locations are implemented
	buttonSize := float32(int(ep.toolbarButtonScale(ctx) * toolbarButtonSize + 0.5)) // Check all of these sizes
	toolbarDrawState.drawStartPos = [2]float32{0, ctx.PaneExtent.Height() - 1}
	drawEndPos := [2]float32{ctx.PaneExtent.Width(), toolbarDrawState.drawStartPos[1] - buttonSize}

	toolbarDrawState.cursor = toolbarDrawState.drawStartPos

	toolbarDrawState.style = renderer.TextStyle{
		Font:        renderer.GetDefaultFont(), // TODO: get the right font
		Color:       toolbarTextColor,
		LineSpacing: 0,
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)

	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	trid.AddQuad(toolbarDrawState.drawStartPos, [2]float32{drawEndPos[0], toolbarDrawState.drawStartPos[1]},
		drawEndPos, [2]float32{toolbarDrawState.drawStartPos[0], drawEndPos[1]}, renderer.RGB{.31, .31, .31})
	trid.GenerateCommands(cb)

	if ctx.Mouse != nil && ctx.Mouse.Clicked[platform.MouseButtonPrimary] {
		toolbarDrawState.mouseDownPos = ctx.Mouse.Pos[:]
	}
}

func drawToolbarText(text string, td *renderer.TextDrawBuilder, buttonSize [2]float32, color renderer.RGB) {
	// Clean up the text
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}

	style := toolbarDrawState.style
	style.Color = renderer.LerpRGB(.5, color, toolbarDrawState.brightness.ScaleRGB(color))
	_, h := style.Font.BoundText(strings.Join(lines, "\n"), toolbarDrawState.style.LineSpacing)

	slop := buttonSize[1] - float32(h) // todo: what if negative...
	y0 := toolbarDrawState.cursor[1] - 1 - slop/2
	for _, line := range lines {
		lw, lh := style.Font.BoundText(line, style.LineSpacing)
		// Try to center the text, though if it's too big to fit in the
		// button then draw it starting from the left edge of the button so
		// that the trailing characters are the ones that are lost.
		x0 := toolbarDrawState.cursor[0] + max(1, (buttonSize[0]-float32(lw))/2)

		td.AddText(line, [2]float32{x0, y0}, style)
		y0 -= float32(lh)
	}
}
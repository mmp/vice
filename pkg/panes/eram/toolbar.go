package eram

import (
	"fmt"

	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/util"
)

// Find out how to get these correctly
const toolbarButtonSize = 84
const numToolbarSlots = 17

var toolbarDrawState struct {
	cb           *renderer.CommandBuffer
	mouse        *platform.MouseState
	mouseDownPos []float32
	cursor       [2]float32
	drawStartPos [2]float32
	style        renderer.TextStyle
	brightness   ERAMBrightness
	position     int
}

const (
	toolbarMain = iota 
	toolbarDraw
	toolbarViews
	toolbarATCTools
	toolbarChecklist 
	toolbarABSettings
	toolbarCommandMenu
	toolbarVideomap
	toolbarCursor 
	toolbarBright
	toolbarRadarFilter
	toolbarFont
	toolbarPrefSet
	toolbarDBFields
	toolbarToolbar // This is a floating button; I'll put this here for now, 
	//but we'll see later where it truly belongs.
)

var ( // TODO: Change to actual colors, but these STARS ones will suffice for now. The colors do vary based on button so maybe 
	// a seperate field in each individual button is needed?
	toolbarButtonColor            = renderer.RGB{0, .173, 0}
	toolbarActiveButtonColor      = renderer.RGB{0, .305, 0}
	toolbarTextColor              = renderer.RGB{1, 1, 1}
	toolbarTextSelectedColor      = renderer.RGB{1, 1, 0}
	toolbarUnsupportedButtonColor = renderer.RGB{.4, .4, .4}
	toolbarUnsupportedTextColor   = renderer.RGB{.8, .8, .8}
	toolbarDisabledButtonColor    = renderer.RGB{0, .173 / 2, 0}
	toolbarDisabledTextColor      = renderer.RGB{.5, 0.5, 0.5}
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

func (ep *ERAMPane) drawtoolbar(ctx *panes.Context, transforms ScopeTransformations, cb *renderer.CommandBuffer)  {
	// ps := ep.currentPrefs()

	scale := ep.toolbarButtonScale(ctx)

	ep.startDrawtoolbar(ctx, scale, transforms, cb)
	switch ep.activeToolbarMenu {
		case toolbarMain:
			
	}

}

func (ep *ERAMPane) toolbarButtonScale(ctx *panes.Context) float32 {
	ds := ctx.DrawPixelScale
	return math.Min(ds, (ds*ctx.PaneExtent.Width()-4)/(numToolbarSlots*toolbarButtonSize))
	}

func (ep *ERAMPane) drawToolbarButton(ctx *panes.Context, text string, flags toolbarFlags, buttonScale float32, pushedIn bool) bool {
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

		disabled := flags&buttonDisabled != 0
		if disabled {
			buttonColor = toolbarDisabledButtonColor
			textColor = toolbarDisabledTextColor
		}
		unsupported := flags&buttonUnsupported != 0
		if unsupported {
			buttonColor = toolbarUnsupportedButtonColor
			textColor = toolbarUnsupportedTextColor
		}
		if !disabled && !unsupported {
			if mouseInside && mouseDownInside {
				pushedIn = !pushedIn
			}
	
			// Swap selected/regular color to indicate the tentative result
			if flags&buttonWXAVL != 0 {
				buttonColor = util.Select(pushedIn, renderer.RGBFromUInt8(116, 116, 162), // 70,70,100
					renderer.RGBFromUInt8(83, 83, 162)) // 50,50,100
			} else {
				buttonColor = util.Select(pushedIn, toolbarActiveButtonColor, toolbarButtonColor)
			}
			textColor = util.Select(mouseInside, toolbarTextSelectedColor, toolbarTextColor)
		}
		buttonColor = toolbarDrawState.brightness.ScaleRGB(buttonColor)
	//textColor = dcbDrawState.brightness.ScaleRGB(textColor)

	trid.AddQuad(p0, p1, p2, p3, buttonColor)
	drawDCBText(text, td, sz, textColor)

	// Draw the bevel around the button
	topLeftBevelColor := renderer.RGB{0.2, 0.2, 0.2}
	bottomRightBevelColor := renderer.RGB{0, 0, 0}
	shiftp := func(p [2]float32, dx, dy float32) [2]float32 {
		return math.Add2f(p, [2]float32{dx, dy})
	}
	if !disabled && !unsupported && pushedIn { //((selected && !mouseInside) || (!selected && mouseInside && mouse.Down[MouseButtonPrimary])) {
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
	winBase := math.Add2f(toolbarDrawState.cursor, ctx.PaneExtent.P0)
	toolbarDrawState.cb.SetScissorBounds(math.Extent2D{
		P0: [2]float32{winBase[0], winBase[1] - sz[1]},
		P1: [2]float32{winBase[0] + sz[0], winBase[1]},
	}, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	moveDCBCursor(flags, sz, ctx)

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

func (ep *ERAMPane) startDrawtoolbar(ctx *panes.Context, buttonScale float32, transforms ScopeTransformations,
	cb *renderer.CommandBuffer) {
	toolbarDrawState.cb = cb
	toolbarDrawState.mouse = ctx.Mouse

	ps := ep.currentPrefs()
	toolbarDrawState.brightness = ps.Brightness.Toolbar
	var drawEndPos [2]float32
	// TODO: position (i think)

	toolbarDrawState.cursor = toolbarDrawState.drawStartPos

	toolbarDrawState.style = renderer.TextStyle{
		Font: renderer.GetDefaultFont(), // TODO: get the right font
		Color: renderer.RGB{R: 1, G: 1, B: 1},
		LineSpacing: 0,
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)

	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	trid.AddQuad(toolbarDrawState.drawStartPos, [2]float32{drawEndPos[0], toolbarDrawState.drawStartPos[1]},
	drawEndPos, [2]float32{toolbarDrawState.drawStartPos[0], drawEndPos[1]}, renderer.RGB{0, 0.05, 0})
	trid.GenerateCommands(cb)

	if ctx.Mouse != nil && ctx.Mouse.Clicked[platform.MouseButtonPrimary] {
		toolbarDrawState.mouseDownPos = ctx.Mouse.Pos[:]
	}
}
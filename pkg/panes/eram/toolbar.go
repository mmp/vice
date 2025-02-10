package eram

import (
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
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

func (ep *ERAMPane) startDrawtoolbar(ctx *panes.Context, buttonScale float32, transforms ScopeTransformations,
	cb *renderer.CommandBuffer) {
	toolbarDrawState.cb = cb
	toolbarDrawState.mouse = ctx.Mouse

	ps := ep.currentPrefs()
	toolbarDrawState.brightness = ps.Brightness.Toolbar
	var drawEndPos [2]float32
	// Dont need position or button size. (right?)

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
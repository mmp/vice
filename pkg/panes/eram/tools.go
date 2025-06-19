package eram

import (
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/radar"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/util"
)

var commandDrawState struct {
	cb                   *renderer.CommandBuffer
	mouse                *platform.MouseState
	commandBigPosition   [2]float32
	commandSmallPosition [2]float32
}

func (ep *ERAMPane) drawCommandInput(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	// scale := 5 // Change this to the correct size (if this is even needed)
	ep.startDrawCommandInput(ctx, transforms, cb)

	// End draw

	// ps := ep.currentPrefs()
	ep.drawSmallCommandOutput(ctx)
	ep.drawBigCommandInput(ctx)

}

func (ep *ERAMPane) startDrawCommandInput(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	commandDrawState.cb = cb
	commandDrawState.mouse = ctx.Mouse
	ps := ep.currentPrefs()
	commandDrawState.commandBigPosition = ps.commandBigPosition
	commandDrawState.commandSmallPosition = ps.commandSmallPosition

	toolbarDrawState.style = renderer.TextStyle{
		Font:        renderer.GetDefaultFont(), // TODO: get the right font
		Color:       toolbarTextColor,
		LineSpacing: 0,
	}

	if ctx.Mouse != nil && (ctx.Mouse.Clicked[platform.MouseButtonPrimary] || ctx.Mouse.Clicked[platform.MouseButtonTertiary]) {
		toolbarDrawState.mouseDownPos = ctx.Mouse.Pos[:]
	}
}

func (ep *ERAMPane) drawBigCommandInput(ctx *panes.Context) {
	ld := renderer.GetColoredLinesDrawBuilder()
	trid := renderer.GetColoredTrianglesDrawBuilder()
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	defer renderer.ReturnTextDrawBuilder(td)

	ps := ep.currentPrefs()
	sz := [2]float32{390, 77}
	p0 := ps.commandBigPosition // top-left of the output box
	p1 := math.Add2f(p0, [2]float32{sz[0], 0})
	p2 := math.Add2f(p1, [2]float32{0, -sz[1]})
	p3 := math.Add2f(p2, [2]float32{-sz[0], 0})
	color := renderer.RGB{0, 0, 0}
	trid.AddQuad(p0, p1, p2, p3, color)
	// Draw the white outline
	color = ps.Brightness.Border.ScaleRGB(renderer.RGB{.914, .914, .914})

	ld.AddLine(p0, p1, color)
	ld.AddLine(p1, p2, color)
	ld.AddLine(p2, p3, color)
	ld.AddLine(p3, p0, color)
	// Draw wrapped output text in the big box
	style := renderer.TextStyle{
		Font:  renderer.GetDefaultFont(),
		Color: ps.Brightness.Text.ScaleRGB(toolbarTextColor),
	}
	bx, _ := style.Font.BoundText("X", 0)
	cols := int(sz[0] / float32(bx))
	out, _ := util.WrapTextNoSpace(ep.bigOutput, cols, 0, true)
	winBase := math.Add2f(ps.commandBigPosition, ctx.PaneExtent.P0)
	commandDrawState.cb.SetScissorBounds(math.Extent2D{
		P0: [2]float32{winBase[0], winBase[1] - sz[1]},
		P1: [2]float32{winBase[0] + sz[0], winBase[1]},
	}, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	td.AddText(out, [2]float32{p0[0] + 2, p0[1] - 2}, style)

	// Draw the smaller top box now.  Size may change if the input text
	// requires more room.
	inputSize := float32(38)
	bx, _ = style.Font.BoundText("X", 0)
	cols = int(sz[0] / float32(bx))
	inText, _ := util.WrapTextNoSpace(ep.Input, cols, 0, true)
	_, h := style.Font.BoundText(inText, style.LineSpacing)
	if float32(h)+4 > inputSize {
		inputSize = float32(h) + 4
	}

	p0[1] = ps.commandBigPosition[1] + inputSize
	sz[1] = inputSize
	p1 = math.Add2f(p0, [2]float32{sz[0], 0})
	p2 = math.Add2f(p1, [2]float32{0, -sz[1]})
	p3 = math.Add2f(p2, [2]float32{-sz[0], 0})
	color = renderer.RGB{0, 0, 0}
	trid.AddQuad(p0, p1, p2, p3, color)
	// Draw the white outline
	color = ps.Brightness.Border.ScaleRGB(renderer.RGB{.914, .914, .914})
	ld.AddLine(p0, p1, color)
	ld.AddLine(p1, p2, color)
	ld.AddLine(p2, p3, color)
	ld.AddLine(p3, p0, color)

	// Input text inside the small box.  Clip to its extent.
	winBase = math.Add2f([2]float32{ps.commandBigPosition[0], ps.commandBigPosition[1] + inputSize}, ctx.PaneExtent.P0)
	commandDrawState.cb.SetScissorBounds(math.Extent2D{
		P0: [2]float32{winBase[0], winBase[1] - inputSize},
		P1: [2]float32{winBase[0] + sz[0], winBase[1]},
	}, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	td.AddText(inText, [2]float32{p0[0] + 2, p0[1] - 2}, style)

	// Restore the scissor to the pane extent
	commandDrawState.cb.SetScissorBounds(ctx.PaneExtent,
		ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])

	trid.GenerateCommands(commandDrawState.cb)
	ld.GenerateCommands(commandDrawState.cb)
	td.GenerateCommands(commandDrawState.cb)

}

func (ep *ERAMPane) drawSmallCommandOutput(ctx *panes.Context) {
	ld := renderer.GetColoredLinesDrawBuilder()
	trid := renderer.GetColoredTrianglesDrawBuilder()
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	defer renderer.ReturnTextDrawBuilder(td)

	ps := ep.currentPrefs()
	sz := [2]float32{325, 77}
	// p0 := [2]float32{ctx.PaneExtent.Width()/2,ctx.PaneExtent.Height()/2} // commandDrawState.commandSmallPosition
	p0 := ps.commandSmallPosition
	p1 := math.Add2f(p0, [2]float32{sz[0], 0})
	p2 := math.Add2f(p1, [2]float32{0, -sz[1]})
	p3 := math.Add2f(p2, [2]float32{-sz[0], 0})
	color := renderer.RGB{0, 0, 0}
	trid.AddQuad(p0, p1, p2, p3, color)
	// Draw the white outline
	color = ps.Brightness.Border.ScaleRGB(renderer.RGB{.914, .914, .914})

	ld.AddLine(p0, p1, color)
	ld.AddLine(p1, p2, color)
	ld.AddLine(p2, p3, color)
	ld.AddLine(p3, p0, color)
	// Draw wrapped text output in the box
	style := renderer.TextStyle{
		Font:  renderer.GetDefaultFont(),
		Color: ps.Brightness.Text.ScaleRGB(toolbarTextColor),
	}
	bx, _ := style.Font.BoundText("X", 0)
	cols := int(sz[0] / float32(bx))
	out, _ := util.WrapTextNoSpace(ep.smallOutput, cols, 0, true)
	winBase := math.Add2f(ps.commandSmallPosition, ctx.PaneExtent.P0)
	commandDrawState.cb.SetScissorBounds(math.Extent2D{
		P0: [2]float32{winBase[0], winBase[1] - sz[1]},
		P1: [2]float32{winBase[0] + sz[0], winBase[1]},
	}, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	td.AddText(out, [2]float32{p0[0] + 2, p0[1] - 2}, style)
	
	// Restore scissor
	commandDrawState.cb.SetScissorBounds(ctx.PaneExtent,
		ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])

	trid.GenerateCommands(commandDrawState.cb)
	ld.GenerateCommands(commandDrawState.cb)
	td.GenerateCommands(commandDrawState.cb)
}

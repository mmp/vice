/*
TODO:
1. Fix mouse to put top box not the bottom box
*/
package eram

import (
	"time"

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
		Font:        ep.ERAMInputFont(),
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

	// For extent2D, save the top left of the input and bottem right of the output

	ps := ep.currentPrefs()
	sz := [2]float32{390, 77}
	p0 := ps.commandBigPosition // top-left of the output box
	p1 := math.Add2f(p0, [2]float32{sz[0], 0})
	p2 := math.Add2f(p1, [2]float32{0, -sz[1]})
	e2 := p2
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
		Font:  ep.ERAMInputFont(),
		Color: ps.Brightness.Text.ScaleRGB(toolbarTextColor),
	}
	bx, _ := style.Font.BoundText("X", 0)
	cols := int(sz[0] / float32(bx))
	out, _ := util.WrapTextNoSpace(ep.bigOutput.String(), cols, 0, true)
	ep.bigOutput.formatWrap(ps, out)
	winBase := math.Add2f(ps.commandBigPosition, ctx.PaneExtent.P0)
	commandDrawState.cb.SetScissorBounds(math.Extent2D{
		P0: [2]float32{winBase[0], winBase[1] - sz[1]},
		P1: [2]float32{winBase[0] + sz[0], winBase[1]},
	}, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	ep.writeText(td, ep.bigOutput, [2]float32{p0[0] + 2, p0[1] - 2})

	// Draw the smaller top box now.  Size may change if the input text
	// requires more room.
	inputSize := float32(38)
	bx, _ = style.Font.BoundText("X", 0)
	cols = int(sz[0] / float32(bx))
	input := ep.Input.String() + "_"
	inText, _ := util.WrapTextNoSpace(input, cols, 0, true)
	_, h := style.Font.BoundText(inText, style.LineSpacing)
	if float32(h)+4 > inputSize {
		inputSize = float32(h) + 4
	}

	p0[1] = ps.commandBigPosition[1] + inputSize
	sz[1] = inputSize
	p1 = math.Add2f(p0, [2]float32{sz[0], 0})
	p2 = math.Add2f(p1, [2]float32{0, -sz[1]})
	e0 := p0
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

	extent := math.Extent2DFromPoints([][2]float32{e0, e2})
	mouse := ctx.Mouse
	mouseInside := mouse != nil && extent.Inside(mouse.Pos)
	if mouse != nil {
		if (mouseInside && mouse.Clicked[platform.MouseButtonPrimary]) != ep.repositionLargeInput {
			if !ep.repositionLargeInput {
				ep.timeSinceRepo = time.Now() // only do it on first click
			}
			extent := ctx.PaneExtent
			extent.P1[1] -= 115 // Adjust the extent to the top box
			ctx.Platform.StartCaptureMouse(extent)
			ep.repositionLargeInput = true
			// Draw the outline of the box starting from the cursor as the top left corner.
			sz = [2]float32{390, 115} // Size of the entire command input box
			p0 = mouse.Pos
			p1 = math.Add2f(p0, [2]float32{sz[0], 0})
			p2 = math.Add2f(p1, [2]float32{0, -sz[1]})
			p3 = math.Add2f(p2, [2]float32{-sz[0], 0})
			color = renderer.RGB{1, 1, 1} // White outline. TODO: Check if brightness affects this.
			ld.AddLine(p0, p1, color)
			ld.AddLine(p1, p2, color)
			ld.AddLine(p2, p3, color)
			ld.AddLine(p3, p0, color)

		}
		if (mouse.Clicked[platform.MouseButtonPrimary] || mouse.Clicked[platform.MouseButtonTertiary]) && ep.repositionLargeInput &&
			time.Since(ep.timeSinceRepo) > 100*time.Millisecond {
			// get the mouse position and set the commandBigPosition to that
			ps.commandBigPosition = mouse.Pos
			ps.commandBigPosition[1] -= 38
			ep.repositionLargeInput = false
			ctx.Platform.EndCaptureMouse()
		}
	}
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
	style := renderer.TextStyle{
		Font:  ep.ERAMInputFont(),
		Color: ps.Brightness.Text.ScaleRGB(toolbarTextColor),
	}
	inputSize := float32(77)
	bx, _ := style.Font.BoundText("X", 0)
	cols := int(sz[0] / float32(bx))
	inText, _ := util.WrapTextNoSpace(ep.smallOutput.String(), cols, 0, true)
	_, h := style.Font.BoundText(inText, style.LineSpacing)
	if float32(h)+4 > inputSize {
		inputSize = float32(h) + 4
	}
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

	cols = int(sz[0] / float32(bx))
	out, _ := util.WrapTextNoSpace(ep.smallOutput.String(), cols, 0, true)
	ep.smallOutput.formatWrap(ps, out)
	winBase := math.Add2f(ps.commandSmallPosition, ctx.PaneExtent.P0)
	commandDrawState.cb.SetScissorBounds(math.Extent2D{
		P0: [2]float32{winBase[0], winBase[1] - sz[1]},
		P1: [2]float32{winBase[0] + sz[0], winBase[1]},
	}, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	ep.writeText(td, ep.smallOutput, [2]float32{p0[0] + 2, p0[1] - 2})

	// Restore scissor
	commandDrawState.cb.SetScissorBounds(ctx.PaneExtent,
		ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])

	extent := math.Extent2DFromPoints([][2]float32{p0, p2})
	mouse := ctx.Mouse
	mouseInside := mouse != nil && extent.Inside(mouse.Pos)
	if mouse != nil {
		if (mouseInside && mouse.Clicked[platform.MouseButtonPrimary]) != ep.repositionSmallOutput {
			if !ep.repositionSmallOutput {
				ep.timeSinceRepo = time.Now() // only do it on first click
			}
			extent := ctx.PaneExtent
			extent.P1[1] -= 77 // Adjust the extent to the top box
			ctx.Platform.StartCaptureMouse(extent)
			ep.repositionSmallOutput = true
			// Draw the outline of the box starting from the cursor as the top left corner.
			sz = [2]float32{325, 77} // Size of the entire command input box
			p0 = mouse.Pos
			p1 = math.Add2f(p0, [2]float32{sz[0], 0})
			p2 = math.Add2f(p1, [2]float32{0, -sz[1]})
			p3 = math.Add2f(p2, [2]float32{-sz[0], 0})
			color = renderer.RGB{1, 1, 1} // White outline. TODO: Check if brightness affects this.
			ld.AddLine(p0, p1, color)
			ld.AddLine(p1, p2, color)
			ld.AddLine(p2, p3, color)
			ld.AddLine(p3, p0, color)

		}
		if (mouse.Clicked[platform.MouseButtonPrimary] || mouse.Clicked[platform.MouseButtonTertiary]) && ep.repositionSmallOutput &&
			time.Since(ep.timeSinceRepo) > 100*time.Millisecond {
			// get the mouse position and set the commandBigPosition to that
			ps.commandSmallPosition = mouse.Pos
			ep.repositionSmallOutput = false
			ctx.Platform.EndCaptureMouse()
		}
	}

	trid.GenerateCommands(commandDrawState.cb)
	ld.GenerateCommands(commandDrawState.cb)
	td.GenerateCommands(commandDrawState.cb)
}

func (ep *ERAMPane) writeText(td *renderer.TextDrawBuilder, text inputText, loc [2]float32) {
	start0 := loc[0]
	font := ep.ERAMInputFont()
	style := renderer.TextStyle{Font: font}
	for _, char := range text {
		ch := char.char
		if ch != '\n' {
			style.Color = char.color
			loc = td.AddText(string(ch), loc, style)
		} else {
			loc[0] = start0                             // reset the x position
			loc[1] -= float32(font.Size) * float32(1.4) // edit this value
		}

	}
}

// eram/view.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
)

// View is a declarative spec for an ERAM floating window (ALTIM SET, WX REPORT,
// MCA, RA, Clock, CRR). DrawView handles the common chrome — title bar with M /
// minimize buttons and hover outlines, window border, body background, scroll
// bar, drag-to-reposition state machine, and click-consumption ordering — so
// callers only specify dimensions and a body-drawing callback.

// ViewBuilders is passed to a View's Body callback. The draw builders are
// pooled by DrawView and flushed after Body returns; Body should not Generate*
// on them. CB is included so Body may set scissor bounds (MCA/RA need this for
// text clipping); Body must restore the pane-wide scissor bounds before
// returning.
type ViewBuilders struct {
	CB   *renderer.CommandBuffer
	Trid *renderer.ColoredTrianglesDrawBuilder
	Ld   *renderer.ColoredLinesDrawBuilder
	Td   *renderer.TextDrawBuilder
}

// ViewRepoState is caller-owned reposition tracking. One value per view; a
// pane-wide slice (ep.allRepoStates) lets the Escape-key handler cancel any
// in-progress drag uniformly.
type ViewRepoState struct {
	Active     bool
	StartTime  time.Time
	DragOffset [2]float32
}

// Cancel ends an in-progress drag and releases mouse capture.
func (s *ViewRepoState) Cancel(ctx *panes.Context) {
	if s.Active {
		s.Active = false
		ctx.Platform.EndCaptureMouse()
	}
}

// ViewScrollConfig describes the optional scroll bar on the right edge of the
// body. The actual offset lives in the caller-owned ViewScrollState so the
// view's body can read it when rendering. MaxOffset is the largest valid
// State.Offset (in whatever unit the caller uses); the scroll bar is shown
// when MaxOffset > 0 and clicks clamp State.Offset to [0, MaxOffset].
type ViewScrollConfig struct {
	State     *ViewScrollState
	MaxOffset int
}

// ViewScrollState is caller-owned scroll position.
type ViewScrollState struct {
	Offset int
}

// View is the declarative spec. See package overview above.
type View struct {
	Position   *[2]float32
	Reposition *ViewRepoState

	Width      float32
	BodyHeight float32

	Title     string
	TitleFont *renderer.Font

	Opaque     bool
	ShowBorder bool
	Brightness radar.Brightness

	// OpaqueOnlyBg controls how the body background reacts to Opaque. When
	// false (default), the body background is always drawn black — used by
	// every multi-row view (ALTIM SET, WX, CRR, MCA, RA). When true, the body
	// background follows Opaque: grey (scaled by Brightness) when Opaque,
	// not drawn at all when not Opaque. Clock uses this so the scope shows
	// through in T mode.
	OpaqueOnlyBg bool

	OnMenu     func(host math.Extent2D) popup
	OnMinimize func()

	OnBodyTertiaryMenu func(host math.Extent2D) popup

	Body func(extent math.Extent2D, b *ViewBuilders)

	Scroll *ViewScrollConfig
}

// drawRectOutline draws a 1px line-loop around ex.
func drawRectOutline(ld *renderer.ColoredLinesDrawBuilder, ex math.Extent2D, color renderer.RGB) {
	q0 := [2]float32{ex.P0[0], ex.P0[1]}
	q1 := [2]float32{ex.P1[0], ex.P0[1]}
	q2 := [2]float32{ex.P1[0], ex.P1[1]}
	q3 := [2]float32{ex.P0[0], ex.P1[1]}
	ld.AddLine(q0, q1, color)
	ld.AddLine(q1, q2, color)
	ld.AddLine(q2, q3, color)
	ld.AddLine(q3, q0, color)
}

// Scroll-bar geometry (matches the AltimSet layout — canonical).
const (
	scrollBarContentW = float32(14)
	scrollBarBorderW  = float32(1)
	scrollBarTotalW   = scrollBarContentW + 2*scrollBarBorderW
	scrollBarGap      = float32(2)
)

// DrawView renders a View, processing chrome and clicks in this order:
// scroll bar → title-bar buttons → title-bar drag → body-tertiary menu →
// body-primary drag → finalize in-progress drag. Each handler consumes its
// click so Body sees only fall-through events.
func (ep *ERAMPane) DrawView(ctx *panes.Context, transforms radar.ScopeTransformations,
	cb *renderer.CommandBuffer, v View) {

	mouse := ctx.Mouse
	pos := *v.Position
	width := v.Width

	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	hasTitle := v.Title != ""

	// Title-bar height (and the title text style) come from TitleFont.
	var titleH float32
	var titleStyle renderer.TextStyle
	if hasTitle {
		titleStyle = renderer.TextStyle{
			Font:  v.TitleFont,
			Color: v.Brightness.ScaleRGB(colors.view.text),
		}
		titleH = max(16, v.TitleFont.LayoutBounds(v.Title, 0).Height()+4)
	}

	bodyH := v.BodyHeight
	totalH := titleH + bodyH

	// Window corners (p0 = top-left, going clockwise).
	p0 := pos
	p1 := math.Add2f(p0, [2]float32{width, 0})
	p2 := math.Add2f(p1, [2]float32{0, -totalH})
	p3 := math.Add2f(p2, [2]float32{-width, 0})

	// Body extent (in window coords).
	bodyP0 := math.Add2f(p0, [2]float32{0, -titleH})
	bodyP1 := math.Add2f(bodyP0, [2]float32{width, 0})
	bodyP2 := math.Add2f(bodyP1, [2]float32{0, -bodyH})
	bodyP3 := math.Add2f(bodyP0, [2]float32{0, -bodyH})
	bodyExtent := math.Extent2D{P0: bodyP3, P1: bodyP1}

	// Body background and window border (skip when body is empty).
	if bodyH > 0 {
		var bodyBg renderer.RGB
		drawBodyBg := true
		if v.OpaqueOnlyBg {
			if v.Opaque {
				bodyBg = v.Brightness.ScaleRGB(colors.view.opaqueBackground)
			} else {
				drawBodyBg = false
			}
		}
		if drawBodyBg {
			trid.AddQuad(bodyP0, bodyP1, bodyP2, bodyP3, bodyBg)
		}
		if v.ShowBorder {
			borderColor := ep.currentPrefs().Brightness.Border.ScaleRGB(colors.view.border)
			ld.AddLine(p0, p1, borderColor)
			ld.AddLine(p1, p2, borderColor)
			ld.AddLine(p2, p3, borderColor)
			ld.AddLine(p3, p0, borderColor)
		}
	}

	// Title bar.
	var mRect, minRect, titleRect math.Extent2D
	var mouseInsideM, mouseInsideMin, mouseInsideTitle bool
	if hasTitle {
		titleBg := renderer.RGB{}
		if v.Opaque {
			titleBg = colors.view.opaqueBackground
		}
		titleP0 := p0
		titleP1 := math.Add2f(p0, [2]float32{width, 0})
		titleP2 := math.Add2f(titleP1, [2]float32{0, -titleH})
		titleP3 := math.Add2f(p0, [2]float32{0, -titleH})
		trid.AddQuad(titleP0, titleP1, titleP2, titleP3, titleBg)

		const mPad = float32(2)

		if v.OnMenu != nil {
			mw := v.TitleFont.LayoutBounds("M", 0).Width()
			mRect = math.Extent2D{
				P0: [2]float32{titleP0[0], titleP0[1] - titleH},
				P1: [2]float32{titleP0[0] + mPad + mw + mPad, titleP0[1]},
			}
			mouseInsideM = mouse != nil && mRect.Inside(mouse.Pos)
		}
		if v.OnMinimize != nil {
			minw := v.TitleFont.LayoutBounds("-", 0).Width()
			minRect = math.Extent2D{
				P0: [2]float32{titleP1[0] - mPad - minw - mPad, titleP1[1] - titleH},
				P1: titleP1,
			}
			mouseInsideMin = mouse != nil && minRect.Inside(mouse.Pos)
		}

		leftEdge := titleP0[0]
		rightEdge := titleP1[0]
		if v.OnMenu != nil {
			leftEdge = mRect.P1[0]
		}
		if v.OnMinimize != nil {
			rightEdge = minRect.P0[0]
		}
		titleRect = math.Extent2D{
			P0: [2]float32{leftEdge, titleP2[1]},
			P1: [2]float32{rightEdge, titleP1[1]},
		}
		mouseInsideTitle = mouse != nil && titleRect.Inside(mouse.Pos)

		// Title text — bright when title area is hovered.
		titleColor := titleStyle.Color
		if mouseInsideTitle {
			titleColor = colors.view.hoveredOutline
		}
		titleCenter := [2]float32{titleP0[0] + width/2, titleP0[1] - titleH/2}
		td.AddTextCentered(v.Title, titleCenter, renderer.TextStyle{Font: v.TitleFont, Color: titleColor})

		if v.OnMenu != nil {
			mTextColor := titleStyle.Color
			if mouseInsideM {
				mTextColor = colors.view.hoveredOutline
			}
			td.AddTextCentered("M", mRect.Center(),
				renderer.TextStyle{Font: v.TitleFont, Color: mTextColor})
		}
		if v.OnMinimize != nil {
			minTextColor := titleStyle.Color
			if mouseInsideMin {
				minTextColor = colors.view.hoveredOutline
			}
			td.AddTextCentered("-", minRect.Center(),
				renderer.TextStyle{Font: v.TitleFont, Color: minTextColor})
		}

		// Dim outlines first, then bright outlines on hover.
		if v.OnMenu != nil && !mouseInsideM {
			drawRectOutline(ld, mRect, colors.view.buttonOutline)
		}
		if v.OnMinimize != nil && !mouseInsideMin {
			drawRectOutline(ld, minRect, colors.view.buttonOutline)
		}
		if !mouseInsideTitle {
			drawRectOutline(ld, titleRect, colors.view.buttonOutline)
		}
		if v.OnMenu != nil && mouseInsideM {
			drawRectOutline(ld, mRect, colors.view.hoveredOutline)
		}
		if v.OnMinimize != nil && mouseInsideMin {
			drawRectOutline(ld, minRect, colors.view.hoveredOutline)
		}
		if mouseInsideTitle {
			drawRectOutline(ld, titleRect, colors.view.hoveredOutline)
		}
	}

	// Scroll-bar geometry (computed up front so click handling and rendering
	// agree).
	scrollVisible := v.Scroll != nil && v.Scroll.MaxOffset > 0 && bodyH > 0
	var scrollUpRect, scrollDownRect math.Extent2D
	if scrollVisible {
		sectionH := (bodyH - 2 - scrollBarGap) / 2
		scrollX1 := bodyP1[0] - 1
		scrollX0 := scrollX1 - scrollBarTotalW
		upY1 := bodyP0[1] - 1
		upY0 := upY1 - sectionH
		downY0 := upY0 - scrollBarGap
		downY1 := downY0 - sectionH
		scrollUpRect = math.Extent2D{P0: [2]float32{scrollX0, upY0}, P1: [2]float32{scrollX1, upY1}}
		// Note: downY1 < downY0 here (Y increases upward), so P0 = (x0, downY1), P1 = (x1, downY0).
		scrollDownRect = math.Extent2D{P0: [2]float32{scrollX0, downY1}, P1: [2]float32{scrollX1, downY0}}
	}

	// Click handling. Each branch consumes its click via clearMouse*Consumed
	// so Body sees only fall-through events.
	primaryClicked := ep.mousePrimaryClicked(mouse)
	tertiaryClicked := ep.mouseTertiaryClicked(mouse)
	if mouse != nil && (primaryClicked || tertiaryClicked) {
		switch {
		case scrollVisible && scrollUpRect.Inside(mouse.Pos):
			if v.Scroll.State.Offset > 0 {
				v.Scroll.State.Offset--
			}
			ep.clearMousePrimaryConsumed(mouse)
			ep.clearMouseTertiaryConsumed(mouse)

		case scrollVisible && scrollDownRect.Inside(mouse.Pos):
			if v.Scroll.State.Offset < v.Scroll.MaxOffset {
				v.Scroll.State.Offset++
			}
			ep.clearMousePrimaryConsumed(mouse)
			ep.clearMouseTertiaryConsumed(mouse)

		case hasTitle && v.OnMenu != nil && mouseInsideM:
			host := math.Extent2D{P0: [2]float32{p0[0], p2[1]}, P1: [2]float32{p0[0] + width, p0[1]}}
			ep.popup = v.OnMenu(host)
			ep.clearMousePrimaryConsumed(mouse)
			ep.clearMouseTertiaryConsumed(mouse)

		case hasTitle && v.OnMinimize != nil && mouseInsideMin:
			v.OnMinimize()
			ep.clearMousePrimaryConsumed(mouse)
			ep.clearMouseTertiaryConsumed(mouse)

		case hasTitle && mouseInsideTitle && !v.Reposition.Active:
			v.Reposition.Active = true
			v.Reposition.StartTime = time.Now()
			v.Reposition.DragOffset = math.Sub2f(mouse.Pos, p0)
			ctx.Platform.StartCaptureMouse(ctx.PaneExtent)
			ep.clearMousePrimaryConsumed(mouse)
			ep.clearMouseTertiaryConsumed(mouse)

		case !hasTitle && v.OnBodyTertiaryMenu != nil && tertiaryClicked &&
			!v.Reposition.Active && bodyExtent.Inside(mouse.Pos):
			host := math.Extent2D{P0: [2]float32{p0[0], p2[1]}, P1: [2]float32{p0[0] + width, p0[1]}}
			ep.popup = v.OnBodyTertiaryMenu(host)
			ep.clearMouseTertiaryConsumed(mouse)

		case !hasTitle && primaryClicked && bodyExtent.Inside(mouse.Pos) && !v.Reposition.Active:
			v.Reposition.Active = true
			v.Reposition.StartTime = time.Now()
			v.Reposition.DragOffset = math.Sub2f(mouse.Pos, p0)
			ctx.Platform.StartCaptureMouse(ctx.PaneExtent)
			ep.clearMousePrimaryConsumed(mouse)

		case v.Reposition.Active && time.Since(v.Reposition.StartTime) > 100*time.Millisecond:
			*v.Position = math.Sub2f(mouse.Pos, v.Reposition.DragOffset)
			v.Reposition.Active = false
			ctx.Platform.EndCaptureMouse()
			ep.clearMousePrimaryConsumed(mouse)
			ep.clearMouseTertiaryConsumed(mouse)
		}
	}

	// Reposition preview outline.
	if v.Reposition.Active && mouse != nil {
		previewP0 := math.Sub2f(mouse.Pos, v.Reposition.DragOffset)
		previewP1 := math.Add2f(previewP0, [2]float32{width, 0})
		previewP2 := math.Add2f(previewP1, [2]float32{0, -totalH})
		previewP3 := math.Add2f(previewP0, [2]float32{0, -totalH})
		c := colors.view.hoveredOutline
		ld.AddLine(previewP0, previewP1, c)
		ld.AddLine(previewP1, previewP2, c)
		ld.AddLine(previewP2, previewP3, c)
		ld.AddLine(previewP3, previewP0, c)
	}

	// Body content.
	if v.Body != nil && bodyH > 0 {
		v.Body(bodyExtent, &ViewBuilders{CB: cb, Trid: trid, Ld: ld, Td: td})
	}

	// Scroll-bar visual (drawn after Body so it sits on top of body content).
	if scrollVisible {
		sectionH := (bodyH - 2 - scrollBarGap) / 2
		scrollX1 := bodyP1[0] - 1
		scrollX0 := scrollX1 - scrollBarTotalW
		upY1 := bodyP0[1] - 1
		upY0 := upY1 - sectionH
		downY0 := upY0 - scrollBarGap
		downY1 := downY0 - sectionH

		scrollBg := colors.scroll.background
		scrollBorder := colors.scroll.border
		arrowColor := colors.scroll.arrow

		// Up section (top half): a downward-pointing arrow (1,3,5,7,9 wide
		// top to bottom) labels "scroll up".
		upQ := [4][2]float32{
			{scrollX0, upY0}, {scrollX1, upY0}, {scrollX1, upY1}, {scrollX0, upY1},
		}
		trid.AddQuad(upQ[0], upQ[1], upQ[2], upQ[3], scrollBg)
		ld.AddLine(upQ[0], upQ[1], scrollBorder)
		ld.AddLine(upQ[1], upQ[2], scrollBorder)
		ld.AddLine(upQ[2], upQ[3], scrollBorder)
		ld.AddLine(upQ[3], upQ[0], scrollBorder)

		arrowCenterX := scrollX0 + scrollBarBorderW + scrollBarContentW/2
		topArrowTopY := downY1 + 1
		for i, w := range []float32{1, 3, 5, 7, 9} {
			y := topArrowTopY + float32(i)
			ld.AddLine([2]float32{arrowCenterX - w/2, y}, [2]float32{arrowCenterX + w/2, y}, arrowColor)
		}

		// Down section (bottom half): upward-pointing arrow.
		downQ := [4][2]float32{
			{scrollX0, downY0}, {scrollX1, downY0}, {scrollX1, downY1}, {scrollX0, downY1},
		}
		trid.AddQuad(downQ[0], downQ[1], downQ[2], downQ[3], scrollBg)
		ld.AddLine(downQ[0], downQ[1], scrollBorder)
		ld.AddLine(downQ[1], downQ[2], scrollBorder)
		ld.AddLine(downQ[2], downQ[3], scrollBorder)
		ld.AddLine(downQ[3], downQ[0], scrollBorder)

		botArrowTopY := upY1 - 5
		for i, w := range []float32{9, 7, 5, 3, 1} {
			y := botArrowTopY + float32(i)
			ld.AddLine([2]float32{arrowCenterX - w/2, y}, [2]float32{arrowCenterX + w/2, y}, arrowColor)
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

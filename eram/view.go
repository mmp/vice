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

// ViewRepoState is the pane-wide drag-to-reposition state. Only one view can
// be repositioned at a time, so a single instance lives on ERAMPane and
// activeID identifies which view (by View.ID) currently owns the drag.
// activeID is "" when no drag is in progress.
type ViewRepoState struct {
	activeID   string
	startTime  time.Time
	dragOffset [2]float32
}

// Cancel ends an in-progress drag.
func (s *ViewRepoState) Cancel(_ *panes.Context) {
	s.activeID = ""
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

// ViewSelectionState is caller-owned selection tracking for the
// click-to-delete affordance. Selected is the Label of the currently chosen
// row, or "" for none. DrawView clears Selected automatically whenever the
// delete-confirmation popup is no longer the active popup, so dismissal via
// Escape, click-outside, or popup replacement all do the right thing.
type ViewSelectionState struct {
	Selected string
}

// ViewSelectableItem is one selectable row inside the View body. Extent is the
// pane-coord hit-test rectangle; Label is shown in the popup as
// "DELETE <Label>" and identifies the row in the selection state. Labels
// among items in one frame are expected to be unique.
type ViewSelectableItem struct {
	Extent math.Extent2D
	Label  string
}

// ViewSelectable enables hover outlines and click-to-delete on body rows.
// Items is invoked once per frame with the body extent so the caller can
// derive row extents from their RowList without duplicating geometry math.
type ViewSelectable struct {
	State    *ViewSelectionState
	Items    func(body math.Extent2D) []ViewSelectableItem
	OnDelete func(label string)
	Font     *renderer.Font // popup font; nil falls back to View.TitleFont
}

// View is the declarative spec. See package overview above.
type View struct {
	Position *[2]float32

	// ID identifies this view for the pane-wide drag state (ep.viewRepo).
	// Must be unique across all views and stable across frames.
	ID string

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

	Selectable *ViewSelectable
}

// clampViewPos clamps a view top-left position so the whole window stays
// inside the pane, leaving the top-toolbar buffer free when the toolbar is
// visible. pos is the view's top-left in pane-local coords (y up); width
// and totalH are the view's outer dimensions.
func (ep *ERAMPane) clampViewPos(ctx *panes.Context, pos [2]float32, width, totalH float32) [2]float32 {
	paneW := ctx.PaneExtent.Width()
	paneH := ctx.PaneExtent.Height()
	toolbarH := float32(0)
	if ep.currentPrefs().DisplayToolbar {
		toolbarH = buttonSize(buttonFull, ep.toolbarButtonScale(ctx))[1]
	}
	pos[0] = math.Clamp(pos[0], 0, max(0, paneW-width))
	pos[1] = math.Clamp(pos[1], totalH, max(totalH, paneH-toolbarH))
	return pos
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

	// Clamp the position to the pane on every frame so a saved off-screen
	// position can't produce negative scissor boxes or render outside the
	// pane. Write the clamped value back so the caller's storage tracks the
	// constrained position.
	*v.Position = ep.clampViewPos(ctx, *v.Position, width, totalH)
	pos := *v.Position

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

	// Selectable rows: compute extents up front so click handling and the
	// hover-outline pass agree. Also forget any stale selection if the delete
	// popup is no longer the active popup (Escape, click-outside, or another
	// popup replacing it).
	var selItems []ViewSelectableItem
	hoveredSelIdx := -1
	selectableActive := v.Selectable != nil && v.Selectable.Items != nil && bodyH > 0
	if selectableActive {
		selItems = v.Selectable.Items(bodyExtent)
		if mouse != nil {
			for i, it := range selItems {
				if it.Extent.Inside(mouse.Pos) {
					hoveredSelIdx = i
					break
				}
			}
		}
		if state := v.Selectable.State; state != nil && state.Selected != "" {
			if dp, ok := ep.popup.(*deleteEntryPopup); !ok || dp.owner != state {
				state.Selected = ""
			}
		}
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

		case hasTitle && mouseInsideTitle && ep.viewRepo.activeID == "":
			ep.viewRepo.activeID = v.ID
			ep.viewRepo.startTime = time.Now()
			ep.viewRepo.dragOffset = math.Sub2f(mouse.Pos, p0)
			ep.clearMousePrimaryConsumed(mouse)
			ep.clearMouseTertiaryConsumed(mouse)

		case selectableActive && primaryClicked && hoveredSelIdx >= 0:
			item := selItems[hoveredSelIdx]
			v.Selectable.State.Selected = item.Label
			popupFont := v.Selectable.Font
			if popupFont == nil {
				popupFont = v.TitleFont
			}
			ep.popup = ep.openDeleteEntryPopup(ctx, item, v.Selectable, popupFont)
			ep.clearMousePrimaryConsumed(mouse)

		case !hasTitle && v.OnBodyTertiaryMenu != nil && tertiaryClicked &&
			ep.viewRepo.activeID == "" && bodyExtent.Inside(mouse.Pos):
			host := math.Extent2D{P0: [2]float32{p0[0], p2[1]}, P1: [2]float32{p0[0] + width, p0[1]}}
			ep.popup = v.OnBodyTertiaryMenu(host)
			ep.clearMouseTertiaryConsumed(mouse)

		case !hasTitle && primaryClicked && bodyExtent.Inside(mouse.Pos) && ep.viewRepo.activeID == "":
			ep.viewRepo.activeID = v.ID
			ep.viewRepo.startTime = time.Now()
			ep.viewRepo.dragOffset = math.Sub2f(mouse.Pos, p0)
			ep.clearMousePrimaryConsumed(mouse)

		case ep.viewRepo.activeID == v.ID && time.Since(ep.viewRepo.startTime) > 100*time.Millisecond:
			*v.Position = ep.clampViewPos(ctx, math.Sub2f(mouse.Pos, ep.viewRepo.dragOffset), width, totalH)
			ep.viewRepo.activeID = ""
			ep.clearMousePrimaryConsumed(mouse)
			ep.clearMouseTertiaryConsumed(mouse)
		}
	}

	// Reposition preview outline. When the cursor leaves the pane ctx.Mouse
	// goes nil — but the drag is still in progress, so query the platform
	// directly for the cursor position and convert into pane-local coords.
	// clampViewPos pins the preview to the closest valid edge.
	if ep.viewRepo.activeID == v.ID {
		var mousePos [2]float32
		if mouse != nil {
			mousePos = mouse.Pos
		} else {
			mousePos = ctx.WindowToPane(ctx.Platform.GetMouse().Pos)
		}
		previewP0 := ep.clampViewPos(ctx, math.Sub2f(mousePos, ep.viewRepo.dragOffset), width, totalH)
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

	// Hover outline for selectable rows. Skip when the row is also the
	// currently-selected one — its highlight fill carries the visual instead.
	if selectableActive && hoveredSelIdx >= 0 {
		hovered := selItems[hoveredSelIdx]
		state := v.Selectable.State
		if state == nil || hovered.Label != state.Selected {
			drawRectOutline(ld, hovered.Extent, colors.view.hoveredOutline)
		}
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

// deleteEntryPopup is the "DELETE <label>" confirmation popup opened when the
// user clicks a selectable row in a View. owner is the selection state of the
// view that opened it — DrawView checks identity to know when to clear the
// row highlight after dismissal.
type deleteEntryPopup struct {
	origin   [2]float32
	width    float32
	height   float32
	label    string
	owner    *ViewSelectionState
	font     *renderer.Font
	onDelete func(label string)
}

// openDeleteEntryPopup positions a deleteEntryPopup adjacent to the clicked
// row, flipping left if it would otherwise overflow the pane, and warps the
// cursor to its center so the user can confirm without moving the mouse.
func (ep *ERAMPane) openDeleteEntryPopup(ctx *panes.Context, item ViewSelectableItem,
	sel *ViewSelectable, font *renderer.Font) *deleteEntryPopup {

	text := "DELETE " + item.Label
	pad := float32(8)
	width := font.LayoutBounds(text, 0).Width() + 2*pad
	height := font.LayoutBounds("0", 0).Height() + pad

	pe := ctx.PaneExtent
	gap := float32(4)
	rowCenterY := (item.Extent.P0[1] + item.Extent.P1[1]) / 2
	origin := [2]float32{item.Extent.P1[0] + gap, rowCenterY + height/2}
	if origin[0]+width > pe.P1[0] {
		origin[0] = item.Extent.P0[0] - gap - width
	}
	if origin[0] < pe.P0[0] {
		origin[0] = pe.P0[0]
	}
	if origin[1]-height < pe.P0[1] {
		origin[1] = pe.P0[1] + height
	}
	if origin[1] > pe.P1[1] {
		origin[1] = pe.P1[1]
	}

	ctx.SetMousePosition([2]float32{origin[0] + width/2, origin[1] - height/2})

	return &deleteEntryPopup{
		origin:   origin,
		width:    width,
		height:   height,
		label:    item.Label,
		owner:    sel.State,
		font:     font,
		onDelete: sel.OnDelete,
	}
}

func (d *deleteEntryPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	mouse := ctx.Mouse
	ps := ep.currentPrefs()
	bButton := ps.Brightness.Button
	bBorder := ps.Brightness.Border
	bText := ps.Brightness.Text

	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	p0 := d.origin
	p1 := [2]float32{p0[0] + d.width, p0[1]}
	p2 := [2]float32{p1[0], p0[1] - d.height}
	p3 := [2]float32{p0[0], p2[1]}
	extent := math.Extent2D{P0: p3, P1: p1}
	hovered := mouse != nil && extent.Inside(mouse.Pos)

	trid.AddQuad(p0, p1, p2, p3, bButton.ScaleRGB(colors.popup.backgroundGrey))

	outline := bBorder.ScaleRGB(colors.menu.rowDimOutline)
	if hovered {
		outline = bBorder.ScaleRGB(colors.menu.rowHoverOutline)
	}
	ld.AddLine(p0, p1, outline)
	ld.AddLine(p1, p2, outline)
	ld.AddLine(p2, p3, outline)
	ld.AddLine(p3, p0, outline)

	td.AddTextCentered("DELETE "+d.label,
		[2]float32{p0[0] + d.width/2, p0[1] - d.height/2},
		renderer.TextStyle{Font: d.font, Color: bText.ScaleRGB(colors.popup.text)})

	transforms.LoadWindowViewingMatrices(cb)
	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)

	if ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse) {
		if hovered && d.onDelete != nil {
			d.onDelete(d.label)
		}
		ep.popup = nil
		ep.clearMousePrimaryConsumed(mouse)
		ep.clearMouseTertiaryConsumed(mouse)
	}
}

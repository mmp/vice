package eram

import (
	"fmt"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"
)

// ERAMMenu - reusable floating popup menu component: streamlines making the many slightly different menus in ERAM.

// popup is the interface implemented by every floating pop-up menu. ERAMPane
// holds at most one (in ep.popup); opening a new pop-up replaces whatever was
// there.
type popup interface {
	draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer)
}

// popupAnchorSide identifies which edge of the host view is pinned by an
// open view-spawned pop-up. While the pop-up is open, DrawView re-derives
// the view's horizontal position so the pinned edge stays put, regardless
// of width changes driven by the pop-up's settings.
type popupAnchorSide int

const (
	popupAnchorNone popupAnchorSide = iota
	popupAnchorLeft
	popupAnchorRight
)

// viewPopupPlacement is the result of OpenPopupAt: the final pop-up origin
// plus the anchor side and pinned X derived from where the pop-up landed
// relative to the host view.
type viewPopupPlacement struct {
	Origin [2]float32
	Anchor popupAnchorSide
	PinX   float32
}

// toPopupBase fills a popupBase from this placement plus the host view's ID.
// Removes the identical 4-field struct literal that every popup constructor
// used to write inline.
func (pl viewPopupPlacement) toPopupBase(viewID string) popupBase {
	return popupBase{
		origin: pl.Origin,
		viewID: viewID,
		anchor: pl.Anchor,
		pinX:   pl.PinX,
	}
}

// viewPopupWidth and viewPopupItemH are the canonical dimensions of every
// view-spawned configuration menu: a fixed 150 px width and a 18 px row
// height. Both DrawERAMMenu and makeViewMenu use these; callers don't pick
// either value.
const (
	viewPopupWidth = float32(150)
	viewPopupItemH = float32(18)
)

// makeViewMenu builds the standard View.OnMenu closure: don't open a new
// popup if one is already open for this view, otherwise place a new one
// using OpenPopupAt and wrap its placement into a typed popup. rowCount is
// the number of content rows below the title bar — the helper adds 1 for
// the title bar and multiplies by viewPopupItemH to get the popup height.
func (ep *ERAMPane) makeViewMenu(ctx *panes.Context, viewID string, rowCount int, wrap func(popupBase) popup) func(host math.Extent2D) popup {
	return func(host math.Extent2D) popup {
		if vap, ok := ep.popup.(viewAnchoredPopup); ok {
			if id, _, _ := vap.viewAnchor(); id == viewID {
				return nil
			}
		}
		popupH := float32(rowCount+1) * viewPopupItemH
		pl := ep.OpenPopupAt(ctx, [2]float32{host.P1[0], host.P1[1]},
			viewPopupWidth, popupH, ep.ERAMFont(2), host)
		return wrap(pl.toPopupBase(viewID))
	}
}

// popupBase is embedded by every view-spawned pop-up. It carries the
// placement info needed by DrawView to keep the host view's pinned edge
// flush with the pop-up as the view resizes.
type popupBase struct {
	origin [2]float32
	viewID string
	anchor popupAnchorSide
	pinX   float32
}

func (p popupBase) viewAnchor() (string, popupAnchorSide, float32) {
	return p.viewID, p.anchor, p.pinX
}

// viewAnchoredPopup is implemented by pop-ups that pin a host view's edge.
// DrawView checks for this on ep.popup to decide whether to override the
// view's horizontal position.
type viewAnchoredPopup interface {
	popup
	viewAnchor() (viewID string, side popupAnchorSide, pinX float32)
}

// ERAMMenuClickType distinguishes primary from tertiary clicks.
type ERAMMenuClickType int

const (
	MenuClickPrimary ERAMMenuClickType = iota
	MenuClickTertiary
)

// ERAMMenuItem describes a single row in the menu, or a scrollable list section if SubRows is set.
type ERAMMenuItem struct {
	Label    string
	BgColor  renderer.RGB
	Color    renderer.RGB
	Centered bool
	OnClick  func(clickType ERAMMenuClickType) bool // return true = close menu

	// If non-empty, drawn after Label inside an outlined box (Color is used for both the text and the box).
	BoxedSuffix string

	// If SubRows is non-nil, this row renders as a scrollable list instead of a single text row.
	SubRows     []ERAMScrollItem
	VisibleRows int                                          // Number of visible sub-rows; 0 = show all
	ScrollState *ERAMScrollState                             // Required if SubRows is non-nil
	OnSelect    func(index int, clickType ERAMMenuClickType) // Called when a sub-row is clicked
}

// ERAMScrollState is caller-owned persistent state for a scrollable list.
type ERAMScrollState struct {
	Offset      int // first visible item index
	SelectedIdx int // -1 = none
}

// ERAMScrollItem describes one item in a scrollable list.
type ERAMScrollItem struct {
	Label string
	Color renderer.RGB
}

// makeBooleanMenuItem builds a centered toggle row whose label flips between
// trueLabel and falseLabel based on *v. Background is grey when *v is true,
// black otherwise. Click toggles *v.
func (ep *ERAMPane) makeBooleanMenuItem(v *bool, trueLabel, falseLabel string) ERAMMenuItem {
	return ERAMMenuItem{
		Label:    util.Select(*v, trueLabel, falseLabel),
		BgColor:  util.Select(*v, colors.popup.backgroundGrey, colors.popup.backgroundBlack),
		Color:    colors.popup.text,
		Centered: true,
		OnClick: func(_ ERAMMenuClickType) bool {
			*v = !*v
			return false
		},
	}
}

// makeToggleMenuItem builds a left-justified toggle row with a fixed label.
// Background is grey when *v is true, black otherwise. Click toggles *v.
func (ep *ERAMPane) makeToggleMenuItem(v *bool, label string) ERAMMenuItem {
	return ERAMMenuItem{
		Label:   label,
		BgColor: util.Select(*v, colors.popup.backgroundGrey, colors.popup.backgroundBlack),
		Color:   colors.popup.text,
		OnClick: func(_ ERAMMenuClickType) bool {
			*v = !*v
			return false
		},
	}
}

// makeIntMenuItem builds an int-adjustment row labeled "<label> <value>" with a
// green background. Primary click decrements, tertiary click increments by step,
// clamped to [min, max]. Generic over any int-kinded type so callers can pass
// e.g. *radar.Brightness directly. Free-standing because Go methods cannot have
// their own type parameters.
func makeIntMenuItem[T ~int](ep *ERAMPane, v *T, label string, min, max, step int) ERAMMenuItem {
	return ERAMMenuItem{
		Label:   fmt.Sprintf("%s %d", label, *v),
		BgColor: colors.popup.backgroundGreen,
		Color:   colors.popup.text,
		OnClick: func(_ ERAMMenuClickType) bool {
			handleClick(ep, v, min, max, step)
			return false
		},
	}
}

// ERAMMenuConfig holds the full configuration for a menu drawn by DrawERAMMenu.
type ERAMMenuConfig struct {
	Title              string
	TitleLeftJustified bool // false = centered (default)
	ShowMButton        bool
	OnMClick           func(ERAMMenuClickType)
	Width              float32
	Font               *renderer.Font // row font; nil = ep.ERAMToolbarFont()
	TitleFont          *renderer.Font // title bar font; nil = same as Font
	ItemHeight         float32        // 0 = default 18px
	Rows               []ERAMMenuItem

	// Escape hatch for custom drawn content (e.g. CRR color swatches).
	// Returns the new cursor position after drawing.
	CustomContent func(cursor [2]float32, width float32,
		trid *renderer.ColoredTrianglesDrawBuilder,
		ld *renderer.ColoredLinesDrawBuilder,
		td *renderer.TextDrawBuilder,
		mouse *platform.MouseState) [2]float32
}

// OpenPopupAt clamps originGuess so a menu with the given dimensions stays on
// screen — preferring to flip to the left of hostExtent when the popup would
// extend past the right edge of the pane — and warps the cursor to the center
// of the title-bar close (X) button. The returned placement carries the
// clamped origin plus the anchor side and pinned X that DrawView uses to
// keep the host view's pinned edge flush with the pop-up as the view
// resizes.
func (ep *ERAMPane) OpenPopupAt(ctx *panes.Context, originGuess [2]float32, width, height float32, titleFont *renderer.Font, hostExtent math.Extent2D) viewPopupPlacement {
	pe := ctx.PaneExtent
	origin := originGuess
	anchor := popupAnchorRight
	pinX := hostExtent.P1[0]
	if origin[0]+width > pe.P1[0] {
		if hostExtent.Width() > 0 && hostExtent.P0[0]-width >= pe.P0[0] {
			origin[0] = hostExtent.P0[0] - width
			anchor = popupAnchorLeft
			pinX = hostExtent.P0[0]
		} else {
			origin[0] = pe.P1[0] - width
			anchor = popupAnchorRight
			pinX = origin[0]
		}
	}
	if origin[0] < pe.P0[0] {
		origin[0] = pe.P0[0]
		anchor = popupAnchorLeft
		pinX = origin[0] + width
	}
	if origin[1]-height < pe.P0[1] {
		origin[1] = pe.P0[1] + height
	}
	if origin[1] > pe.P1[1] {
		origin[1] = pe.P1[1]
	}

	// Title bar X button (DrawERAMMenu draws it at width - xPad - xw, with
	// itemH-tall title bar). The cursor lands at the visual center.
	const itemH = float32(18)
	const xPad = float32(2)
	xw := titleFont.LayoutBounds("X", 0).Width()
	ctx.SetMousePosition([2]float32{
		origin[0] + width - xPad - xw/2,
		origin[1] - itemH/2,
	})

	return viewPopupPlacement{Origin: origin, Anchor: anchor, PinX: pinX}
}

// ERAMMenuResult is returned by DrawERAMMenu.
type ERAMMenuResult struct {
	Extent     math.Extent2D
	Dismissed  bool
	RowExtents []math.Extent2D
}

// DrawERAMMenu renders a floating popup menu and handles clicks.
func (ep *ERAMPane) DrawERAMMenu(ctx *panes.Context, transforms radar.ScopeTransformations,
	cb *renderer.CommandBuffer, origin [2]float32, cfg ERAMMenuConfig) ERAMMenuResult {

	var result ERAMMenuResult

	// Defaults
	font := cfg.Font
	if font == nil {
		font = ep.ERAMToolbarFont()
	}
	itemH := cfg.ItemHeight
	if itemH == 0 {
		itemH = 18
	}
	width := cfg.Width

	mouse := ctx.Mouse
	ps := ep.currentPrefs()
	bButton := ps.Brightness.Button
	bBorder := ps.Brightness.Border
	bText := ps.Brightness.Text
	textColor := bText.ScaleRGB(colors.menu.titleText)

	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	titleFont := cfg.TitleFont
	if titleFont == nil {
		titleFont = font
	}

	cursor := origin

	// Title bar
	{
		rp0 := cursor
		rp1 := math.Add2f(cursor, [2]float32{width, 0})
		rp2 := math.Add2f(rp1, [2]float32{0, -itemH})
		rp3 := math.Add2f(rp0, [2]float32{0, -itemH})
		trid.AddQuad(rp0, rp1, rp2, rp3, bButton.ScaleRGB(colors.menu.titleBackground))

		style := renderer.TextStyle{Font: titleFont, Color: textColor}

		if cfg.TitleLeftJustified {
			th := titleFont.LayoutBounds(cfg.Title, 0).Height()
			textY := rp0[1] - itemH/2 + th/2
			td.AddText(cfg.Title, [2]float32{rp0[0] + 4, textY}, style)
		} else {
			centerX := rp0[0] + width/2
			centerY := rp0[1] - itemH/2
			td.AddTextCentered(cfg.Title, [2]float32{centerX, centerY}, style)
		}

		// Close button on right
		xLabel := "X"
		xw := titleFont.LayoutBounds(xLabel, 0).Width()
		xPos := math.Add2f(cursor, [2]float32{width - xw - 2, -1})
		td.AddText(xLabel, xPos, style)

		// Separator line at bottom of title
		ld.AddLine(rp0, rp1, bBorder.ScaleRGB(colors.menu.rowDimOutline))
		// Left border of title
		ld.AddLine(cursor, [2]float32{cursor[0], rp3[1]}, colors.menu.titleLeftBorder)

		// Build extents for M button (optional) and X button
		var mRect, xRect, titleRect math.Extent2D

		if cfg.ShowMButton {
			mLabel := "M"
			mext := titleFont.LayoutBounds(mLabel, 0)
			mw, mh := mext.Width(), mext.Height()
			mPad := float32(2)
			mRect = math.Extent2D{
				P0: [2]float32{rp0[0], rp0[1] - itemH},
				P1: [2]float32{rp0[0] + mPad + mw + mPad, rp0[1]},
			}
			td.AddText(mLabel, math.Add2f(rp0, [2]float32{mPad, -itemH/2 + mh/2}), style)
		}

		xPad := float32(2)
		xRect = math.Extent2D{
			P0: [2]float32{rp1[0] - xPad - xw - xPad, rp1[1] - itemH},
			P1: rp1,
		}

		leftEdge := rp0[0]
		if cfg.ShowMButton {
			leftEdge = mRect.P1[0]
		}
		titleRect = math.Extent2D{
			P0: [2]float32{leftEdge, rp2[1]},
			P1: [2]float32{xRect.P0[0], rp1[1]},
		}

		// Hover outlines
		drawMenuRectOutline := func(ex math.Extent2D, color renderer.RGB) {
			q0 := [2]float32{ex.P0[0], ex.P0[1]}
			q1 := [2]float32{ex.P1[0], ex.P0[1]}
			q2 := [2]float32{ex.P1[0], ex.P1[1]}
			q3 := [2]float32{ex.P0[0], ex.P1[1]}
			ld.AddLine(q0, q1, color)
			ld.AddLine(q1, q2, color)
			ld.AddLine(q2, q3, color)
			ld.AddLine(q3, q0, color)
		}

		mouseInsideX := mouse != nil && xRect.Inside(mouse.Pos)
		mouseInsideTitle := mouse != nil && titleRect.Inside(mouse.Pos)
		mouseInsideM := cfg.ShowMButton && mouse != nil && mRect.Inside(mouse.Pos)

		dimOutline := bBorder.ScaleRGB(colors.menu.buttonOutline)
		hotOutline := bBorder.ScaleRGB(colors.menu.buttonHoverOutline)
		// Draw non-hovered outlines first
		if cfg.ShowMButton && !mouseInsideM {
			drawMenuRectOutline(mRect, dimOutline)
		}
		if !mouseInsideX {
			drawMenuRectOutline(xRect, dimOutline)
		}
		if !mouseInsideTitle {
			drawMenuRectOutline(titleRect, dimOutline)
		}
		// Draw hovered outlines last
		if cfg.ShowMButton && mouseInsideM {
			drawMenuRectOutline(mRect, hotOutline)
		}
		if mouseInsideX {
			drawMenuRectOutline(xRect, hotOutline)
		}
		if mouseInsideTitle {
			drawMenuRectOutline(titleRect, hotOutline)
		}

		// Title bar click handling
		if ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse) {
			clickType := MenuClickPrimary
			if ep.mouseTertiaryClicked(mouse) {
				clickType = MenuClickTertiary
			}
			if mouseInsideX {
				ep.popup = nil
				result.Dismissed = true
			} else if cfg.ShowMButton && mouseInsideM {
				if cfg.OnMClick != nil {
					cfg.OnMClick(clickType)
				}
			}
		}

		cursor = rp3
	}

	// Rows (static rows and scroll sections)
	isFirstRow := true
	result.RowExtents = make([]math.Extent2D, len(cfg.Rows))

	for i, item := range cfg.Rows {
		if item.SubRows != nil {
			// Scrollable list section
			cursor = ep.drawScrollSection(cursor, width, itemH, font, &cfg.Rows[i], trid, ld, td, mouse, &result.RowExtents[i])
		} else {
			// Normal static row
			rp0 := cursor
			rp1 := math.Add2f(cursor, [2]float32{width, 0})
			rp2 := math.Add2f(rp1, [2]float32{0, -itemH})
			rp3 := math.Add2f(rp0, [2]float32{0, -itemH})
			trid.AddQuad(rp0, rp1, rp2, rp3, bButton.ScaleRGB(item.BgColor))

			itemTextColor := bText.ScaleRGB(item.Color)
			style := renderer.TextStyle{Font: font, Color: itemTextColor}
			if item.Centered {
				centerX := rp0[0] + width/2
				centerY := rp0[1] - itemH/2
				td.AddTextCentered(item.Label, [2]float32{centerX, centerY}, style)
			} else {
				labelExt := font.LayoutBounds(item.Label, 0)
				labelW, th := labelExt.Width(), labelExt.Height()
				textY := rp0[1] - itemH/2 + th/2
				td.AddText(item.Label, [2]float32{rp0[0] + 4, textY}, style)

				if item.BoxedSuffix != "" {
					sext := font.InkBounds(item.BoxedSuffix, 0)
					boxPad := float32(2)
					boxX0 := rp0[0] + 4 + labelW + 4
					boxX1 := boxX0 + sext.Width() + 2*boxPad
					boxY0 := rp0[1] - itemH + 2
					boxY1 := rp0[1] - 2
					ld.AddLineLoop(bBorder.ScaleRGB(item.Color), [][2]float32{
						{boxX0, boxY0}, {boxX1, boxY0}, {boxX1, boxY1}, {boxX0, boxY1},
					})
					td.AddText(item.BoxedSuffix,
						[2]float32{boxX0 + boxPad - sext.P0[0], textY}, style)
				}
			}

			cursor = rp3
			extent := math.Extent2D{P0: rp3, P1: rp1}
			result.RowExtents[i] = extent

			// Row hover outlines
			dimColor := bBorder.ScaleRGB(colors.menu.rowDimOutline)
			brightColor := bBorder.ScaleRGB(colors.menu.rowHoverOutline)
			hovered := mouse != nil && extent.Inside(mouse.Pos)

			if hovered {
				ld.AddLine(rp0, rp1, brightColor)
				ld.AddLine(rp3, rp2, brightColor)
				ld.AddLine(rp0, rp3, brightColor)
				ld.AddLine(rp1, rp2, brightColor)
			} else {
				if isFirstRow {
					ld.AddLine(rp0, rp1, dimColor)
				}
				ld.AddLine(rp3, rp2, dimColor)
			}
		}
		isFirstRow = false
	}

	// Custom content
	if cfg.CustomContent != nil {
		cursor = cfg.CustomContent(cursor, width, trid, ld, td, mouse)
	}

	// Border
	menuP0 := origin
	menuP1 := [2]float32{origin[0] + width, origin[1]}
	menuP2 := [2]float32{origin[0] + width, cursor[1]}
	menuP3 := [2]float32{origin[0], cursor[1]}

	result.Extent = math.Extent2D{P0: menuP3, P1: menuP1}
	ep.popupExtent = result.Extent

	ld.AddLineLoop(bBorder.ScaleRGB(colors.menu.outerBorder), [][2]float32{menuP0, menuP1, menuP2, menuP3})

	// Flush draw builders
	transforms.LoadWindowViewingMatrices(cb)
	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)

	// Click-outside dismissal
	if ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse) {
		if mouse != nil && !result.Extent.Inside(mouse.Pos) {
			ep.popup = nil
			result.Dismissed = true
		}
	}

	// Row click handling (skip scroll rows - they handle their own clicks).
	// Fires on initial press and then on hold-repeat once a row has been
	// pressed continuously, mirroring the toolbar button behavior so that
	// holding a BRIGHT-style row keeps incrementing.
	if mouse != nil {
		primaryDown := ep.mousePrimaryDown(mouse)
		tertiaryDown := ep.mouseTertiaryDown(mouse)
		if primaryDown || tertiaryDown {
			hitRow := -1
			for i, ext := range result.RowExtents {
				if cfg.Rows[i].SubRows != nil {
					continue // scroll rows handle clicks in drawScrollSection
				}
				if ext.Inside(mouse.Pos) {
					hitRow = i
					break
				}
			}
			if hitRow >= 0 {
				now := time.Now()
				fire := false
				if toolbarDrawState.mouseYetReleased {
					toolbarDrawState.mouseYetReleased = false
					toolbarDrawState.lastHold = now.Add(500 * time.Millisecond)
					fire = true
				} else if now.Sub(toolbarDrawState.lastHold) >= holdDuration {
					toolbarDrawState.lastHold = now
					fire = true
				}
				if fire && cfg.Rows[hitRow].OnClick != nil {
					clickType := MenuClickPrimary
					if tertiaryDown {
						clickType = MenuClickTertiary
					}
					if cfg.Rows[hitRow].OnClick(clickType) {
						result.Dismissed = true
					}
				}
			}
		}
	}

	return result
}

// drawScrollSection renders a scrollable list section for a row with SubRows.
// It handles drawing, scroll arrows, mouse wheel, and click selection.
// Returns the updated cursor position.
func (ep *ERAMPane) drawScrollSection(cursor [2]float32, width, itemH float32, font *renderer.Font,
	item *ERAMMenuItem, trid *renderer.ColoredTrianglesDrawBuilder, ld *renderer.ColoredLinesDrawBuilder,
	td *renderer.TextDrawBuilder, mouse *platform.MouseState, extent *math.Extent2D) [2]float32 {

	ps := ep.currentPrefs()
	bButton := ps.Brightness.Button
	bText := ps.Brightness.Text

	ss := item.ScrollState
	visRows := item.VisibleRows
	if visRows <= 0 {
		visRows = len(item.SubRows)
	}
	scrollH := float32(visRows) * itemH
	arrowW := float32(16)

	// Background
	bgP0 := cursor
	bgP1 := math.Add2f(bgP0, [2]float32{width, 0})
	bgP2 := math.Add2f(bgP1, [2]float32{0, -scrollH})
	bgP3 := math.Add2f(bgP0, [2]float32{0, -scrollH})
	trid.AddQuad(bgP0, bgP1, bgP2, bgP3, bButton.ScaleRGB(item.BgColor))

	scrollExtent := math.Extent2D{P0: bgP3, P1: bgP1}
	*extent = scrollExtent

	// Draw visible items
	for vi := 0; vi < visRows; vi++ {
		idx := ss.Offset + vi
		if idx >= len(item.SubRows) {
			break
		}
		subItem := item.SubRows[idx]
		y := cursor[1] - float32(vi)*itemH

		// Highlight selected
		if idx == ss.SelectedIdx {
			sp0 := [2]float32{cursor[0], y}
			sp1 := [2]float32{cursor[0] + width - arrowW, y}
			sp2 := [2]float32{sp1[0], y - itemH}
			sp3 := [2]float32{cursor[0], y - itemH}
			trid.AddQuad(sp0, sp1, sp2, sp3, bButton.ScaleRGB(colors.menu.selectedItem))
		}

		style := renderer.TextStyle{Font: font, Color: bText.ScaleRGB(subItem.Color)}
		th := font.LayoutBounds(subItem.Label, 0).Height()
		textY := y - itemH/2 + th/2
		td.AddText(subItem.Label, [2]float32{cursor[0] + 4, textY}, style)
	}

	// Scroll arrows (right side)
	canScrollUp := ss.Offset > 0
	canScrollDown := ss.Offset+visRows < len(item.SubRows)

	upCenter := [2]float32{cursor[0] + width - arrowW/2, cursor[1] - itemH/2}
	downCenter := [2]float32{cursor[0] + width - arrowW/2, cursor[1] - scrollH + itemH/2}

	upColor := colors.menu.scrollDimArrow
	if canScrollUp {
		upColor = colors.menu.scrollArrow
	}
	downColor := colors.menu.scrollDimArrow
	if canScrollDown {
		downColor = colors.menu.scrollArrow
	}

	drawUpTriangle(trid, upCenter, 6, upColor)
	drawDownTriangle(trid, downCenter, 6, downColor)

	// Arrow click extents (top half / bottom half of arrow column)
	upExtent := math.Extent2D{
		P0: [2]float32{cursor[0] + width - arrowW, cursor[1] - scrollH/2},
		P1: [2]float32{cursor[0] + width, cursor[1]},
	}
	downExtent := math.Extent2D{
		P0: [2]float32{cursor[0] + width - arrowW, cursor[1] - scrollH},
		P1: [2]float32{cursor[0] + width, cursor[1] - scrollH/2},
	}

	// Mouse wheel scrolling
	if mouse != nil && scrollExtent.Inside(mouse.Pos) {
		if mouse.Wheel[1] > 0 && canScrollUp {
			ss.Offset--
		} else if mouse.Wheel[1] < 0 && canScrollDown {
			ss.Offset++
		}
	}

	// Click handling for scroll
	if ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse) {
		clickType := MenuClickPrimary
		if ep.mouseTertiaryClicked(mouse) {
			clickType = MenuClickTertiary
		}

		if mouse != nil && upExtent.Inside(mouse.Pos) && canScrollUp {
			ss.Offset--
		} else if mouse != nil && downExtent.Inside(mouse.Pos) && canScrollDown {
			ss.Offset++
		} else if mouse != nil && scrollExtent.Inside(mouse.Pos) {
			// Determine which item was clicked
			relY := cursor[1] - mouse.Pos[1]
			clickedVi := int(relY / itemH)
			clickedIdx := ss.Offset + clickedVi
			if clickedIdx >= 0 && clickedIdx < len(item.SubRows) {
				ss.SelectedIdx = clickedIdx
				if item.OnSelect != nil {
					item.OnSelect(clickedIdx, clickType)
				}
			}
		}
	}

	return bgP3
}

// drawUpTriangle draws a filled upward-pointing triangle.
func drawUpTriangle(trid *renderer.ColoredTrianglesDrawBuilder, center [2]float32, size float32, color renderer.RGB) {
	top := [2]float32{center[0], center[1] + size}
	bl := [2]float32{center[0] - size, center[1] - size}
	br := [2]float32{center[0] + size, center[1] - size}
	trid.AddTriangle(top, bl, br, color)
}

// drawDownTriangle draws a filled downward-pointing triangle.
func drawDownTriangle(trid *renderer.ColoredTrianglesDrawBuilder, center [2]float32, size float32, color renderer.RGB) {
	bottom := [2]float32{center[0], center[1] - size}
	tl := [2]float32{center[0] - size, center[1] + size}
	tr := [2]float32{center[0] + size, center[1] + size}
	trid.AddTriangle(bottom, tl, tr, color)
}

// eram/menu.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"fmt"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/util"
)

// Menu - reusable floating popup menu component: streamlines making the many slightly different menus in ERAM.

// popup is the interface implemented by every floating pop-up menu. Scope
// holds at most one (in ep.popup); opening a new pop-up replaces whatever was
// there.
type popup interface {
	draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer)
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

// viewPopupWidth and viewPopupItemH are the canonical dimensions of every
// view-spawned configuration menu: a fixed 150 px width and a 18 px row
// height. Both DrawERAMMenu and openViewMenu use these; callers don't pick
// either value.
const (
	viewPopupWidth = float32(150)
	viewPopupItemH = float32(18)
)

// viewMenu is a view's menu: its title and its rows, which are built again
// each frame. A menu that takes keystrokes also implements keyboardMenu.
type viewMenu interface {
	title() string
	rows(ep *Scope, ctx *scope.Context) []MenuItem
}

// keyboardMenu is a view menu that takes keyboard input while it is
// displayed, as the Group Suppression view's does; handleKeyboard returns
// true if it took the input.
type keyboardMenu interface {
	handleKeyboard(ep *Scope, ctx *scope.Context) bool
}

// viewMenuPopup displays a view's menu beside the view. It carries the
// placement that DrawView uses to keep the view's pinned edge flush with
// the menu as the view resizes.
type viewMenuPopup struct {
	menu   viewMenu
	origin [2]float32
	viewID string
	anchor popupAnchorSide
	pinX   float32
	drawn  bool
}

// openViewMenu displays a view's menu beside the view, or closes it if it
// is displayed already.
func (ep *Scope) openViewMenu(ctx *scope.Context, viewID string, menu viewMenu, host math.Extent2D) {
	if ep.viewMenuDisplayed(viewID) {
		ep.popup = nil
		return
	}
	popupH := float32(len(menu.rows(ep, ctx))+1) * viewPopupItemH
	pl := ep.OpenPopupAt(ctx, [2]float32{host.P1[0], host.P1[1]}, viewPopupWidth, popupH, ep.ERAMFont(2), host)
	ep.popup = &viewMenuPopup{menu: menu, origin: pl.Origin, viewID: viewID, anchor: pl.Anchor, pinX: pl.PinX}
}

// viewMenuDisplayed reports whether the given view's menu is displayed.
func (ep *Scope) viewMenuDisplayed(viewID string) bool {
	if vap, ok := ep.popup.(viewAnchoredPopup); ok {
		id, _, _ := vap.viewAnchor()
		return id == viewID
	}
	return false
}

func (p *viewMenuPopup) viewAnchor() (string, popupAnchorSide, float32) {
	return p.viewID, p.anchor, p.pinX
}

// draw draws the menu. A row places the cursor on itself
// (MenuItem.PlaceCursor) only on the menu's first draw.
func (p *viewMenuPopup) draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	rows := p.menu.rows(ep, ctx)
	if p.drawn {
		for i := range rows {
			rows[i].PlaceCursor = false
		}
	}
	p.drawn = true
	ep.DrawERAMMenu(ctx, transforms, cb, p.origin, MenuConfig{
		Title: p.menu.title(),
		Width: viewPopupWidth,
		Font:  ep.ERAMFont(2),
		Rows:  rows,
	})
}

func (p *viewMenuPopup) handleKeyboard(ep *Scope, ctx *scope.Context) bool {
	if m, ok := p.menu.(keyboardMenu); ok {
		return m.handleKeyboard(ep, ctx)
	}
	return false
}

// cursorPlacement records whether a menu has been drawn, so that it places
// the cursor on the pick area it starts on (MenuItem.PlaceCursor) only on
// its first draw.
type cursorPlacement struct {
	drawn bool
}

// firstDraw reports whether the menu is being drawn for the first time.
func (c *cursorPlacement) firstDraw() bool {
	first := !c.drawn
	c.drawn = true
	return first
}

// viewAnchoredPopup is implemented by pop-ups that pin a host view's edge.
// DrawView checks for this on ep.popup to decide whether to override the
// view's horizontal position.
type viewAnchoredPopup interface {
	popup
	viewAnchor() (viewID string, side popupAnchorSide, pinX float32)
}

// keyboardPopup is implemented by pop-ups that take keyboard input (e.g. the
// free-form text menu). While one is open, processKeyboardInput routes
// keystrokes to it instead of the command input; handleKeyboard returns true
// if it consumed the input.
type keyboardPopup interface {
	popup
	handleKeyboard(ep *Scope, ctx *scope.Context) bool
}

// popupCovers reports whether the displayed pop-up, if any, is over p.
func (ep *Scope) popupCovers(p [2]float32) bool {
	return ep.popup != nil && ep.popupExtent.Inside(p)
}

// MenuClickType distinguishes primary from tertiary clicks.
type MenuClickType int

const (
	MenuClickPrimary MenuClickType = iota
	MenuClickTertiary
)

// MenuItem describes a single row in the menu, or a scrollable list section if SubRows is set.
type MenuItem struct {
	Label    string
	BgColor  renderer.RGB
	Color    renderer.RGB
	Centered bool
	OnClick  func(clickType MenuClickType) bool // return true = close menu

	// Segments are drawn after Label, left to right, and take their own
	// clicks.
	Segments []MenuSegment

	// If SubRows is non-nil, this row renders as a scrollable list instead of a single text row.
	SubRows     []ScrollItem
	VisibleRows int                                      // Number of visible sub-rows; 0 = show all
	ScrollState *ScrollState                             // Required if SubRows is non-nil
	OnSelect    func(index int, clickType MenuClickType) // Called when a sub-row is clicked

	// If Cells is non-nil, this row renders as side-by-side pick areas
	// (e.g. the heading menu's LT | PH | RT row).
	Cells []MenuCell

	// If Grid is non-nil, this row renders as a scrollable multi-column
	// value grid (e.g. the altitude/heading/speed pick lists).
	Grid *MenuGrid

	// If Input is non-nil, this row renders as a text input area. Keyboard
	// handling is the popup's responsibility (see keyboardPopup).
	Input *MenuInput

	// If Custom is non-nil, this row draws its own content (e.g. the CRR
	// menu's color swatches) and takes its own clicks.
	Custom *MenuCustomRow

	// PlaceCursor moves the cursor onto a static row when it is drawn.
	// Menus set it on their first draw to start the cursor on a pick area
	// other than the close button that OpenPopupAt places it on.
	PlaceCursor bool
}

// MenuSegment is a separately clickable run of text in a menu row, drawn in
// an outlined box of its color if Boxed.
type MenuSegment struct {
	Text    string
	Color   renderer.RGB
	Boxed   bool
	OnClick func(clickType MenuClickType) bool // return true = close menu
}

// Spacing of menu row segments: the gap before each one and the padding
// inside its box.
const (
	menuSegmentGap = 4
	menuSegmentPad = 2
)

// menuRowWidth returns the width that a left-justified static row needs for
// its label and segments.
func menuRowWidth(font *renderer.Font, item MenuItem) float32 {
	w := 4 + font.LayoutBounds(item.Label, 0).Width()
	for _, seg := range item.Segments {
		w += menuSegmentGap + font.InkBounds(seg.Text, 0).Width() + 2*menuSegmentPad
	}
	return w + 4
}

// MenuCell is one pick area of a multi-cell row.
type MenuCell struct {
	Label   string
	BgColor renderer.RGB
	Color   renderer.RGB
	Weight  float32                            // relative width; 0 = 1
	OnClick func(clickType MenuClickType) bool // return true = close menu
}

// MenuGridCell is one pick area in a value grid row.
type MenuGridCell struct {
	Label   string
	BgColor renderer.RGB // zero value = section background
	Color   renderer.RGB
	Weight  float32 // relative width; 0 = 1
	// HoverSpansRow extends the hover outline across the row's full content
	// width (e.g. the altitude menu's interim T pick area, whose action
	// includes the altitude to its left).
	HoverSpansRow bool
	PlaceCursor   bool                               // as MenuItem.PlaceCursor
	OnClick       func(clickType MenuClickType) bool // return true = close menu
}

// MenuGrid is a scrollable grid of value pick areas. When the rows don't
// all fit, a reserved arrow column on the right scrolls by pages the way the
// ERAM altitude/heading/speed menus do; the mouse wheel scrolls by rows.
type MenuGrid struct {
	Rows        [][]MenuGridCell
	VisibleRows int  // rows shown per page
	Offset      *int // caller-owned first visible row index
}

// MenuInput is a text input row; the caller owns the buffer and updates
// it from keyboard input.
type MenuInput struct {
	Buf *string
}

// MenuCustomRow is a row of custom content. Draw draws it with its top left
// at cursor and the menu's width, taking any click, and returns its height.
type MenuCustomRow struct {
	Draw func(cursor [2]float32, width float32, b *ViewBuilders, mouse *platform.MouseState) float32
}

// ScrollState is caller-owned persistent state for a scrollable list.
type ScrollState struct {
	Offset      int // first visible item index
	SelectedIdx int // -1 = none
}

// ScrollItem describes one item in a scrollable list.
type ScrollItem struct {
	Label string
	Color renderer.RGB
}

// makeBooleanMenuItem builds a centered toggle row whose label flips between
// trueLabel and falseLabel based on *v. Background is grey when *v is true,
// black otherwise. Click toggles *v.
func (ep *Scope) makeBooleanMenuItem(v *bool, trueLabel, falseLabel string) MenuItem {
	return MenuItem{
		Label:    util.Select(*v, trueLabel, falseLabel),
		BgColor:  util.Select(*v, colors.popup.backgroundGrey, colors.popup.backgroundBlack),
		Color:    colors.popup.text,
		Centered: true,
		OnClick: func(_ MenuClickType) bool {
			*v = !*v
			return false
		},
	}
}

// makeToggleMenuItem builds a left-justified toggle row with a fixed label.
// Background is grey when *v is true, black otherwise. Click toggles *v.
func (ep *Scope) makeToggleMenuItem(v *bool, label string) MenuItem {
	return MenuItem{
		Label:   label,
		BgColor: util.Select(*v, colors.popup.backgroundGrey, colors.popup.backgroundBlack),
		Color:   colors.popup.text,
		OnClick: func(_ MenuClickType) bool {
			*v = !*v
			return false
		},
	}
}

// makeIntMenuItem builds an int-adjustment row labeled "<label> <value>" with a
// green background. Primary click decrements, tertiary click increments by step,
// clamped to [min, max]. Generic over any int-kinded type so callers can pass
// e.g. *scope.Brightness directly. Free-standing because Go methods cannot have
// their own type parameters.
func makeIntMenuItem[T ~int](ep *Scope, v *T, label string, min, max, step int) MenuItem {
	return MenuItem{
		Label:   fmt.Sprintf("%s %d", label, *v),
		BgColor: colors.popup.backgroundGreen,
		Color:   colors.popup.text,
		OnClick: func(click MenuClickType) bool {
			adjustMenuValue(ep, v, click, min, max, step)
			return false
		},
	}
}

// linesAll is the LINES setting, shown as "21+", at which a view grows to
// show all of its entries.
const linesAll = 21

// makeLinesMenuItem builds the LINES row of a view whose setting runs from
// minLines to 20 and then to 21+.
func makeLinesMenuItem(ep *Scope, v *int, minLines int) MenuItem {
	label := fmt.Sprintf("LINES %d", *v)
	if *v >= linesAll {
		label = "LINES 21+"
	}
	return MenuItem{
		Label:   label,
		BgColor: colors.popup.backgroundGreen,
		Color:   colors.popup.text,
		OnClick: func(click MenuClickType) bool {
			*v = min(*v, linesAll)
			adjustMenuValue(ep, v, click, minLines, linesAll, 1)
			return false
		},
	}
}

// visibleLines returns the number of lines a view with the given LINES
// setting shows before it scrolls when it has n entries.
func visibleLines(lines, n int) int {
	if lines >= linesAll {
		return max(n, 1)
	}
	return lines
}

// makeWidthMenuItem builds the WIDTH row of a view that is either narrow or
// wide characters across: TBP selects the narrow width and TBE the wide one.
func makeWidthMenuItem(ep *Scope, v *int, narrow, wide int) MenuItem {
	return MenuItem{
		Label:   fmt.Sprintf("WIDTH %d", *v),
		BgColor: colors.popup.backgroundGreen,
		Color:   colors.popup.text,
		OnClick: func(click MenuClickType) bool {
			w := util.Select(click == MenuClickTertiary, wide, narrow)
			if *v == w {
				ep.reportClickAtLimit(menuToolbarClick(click))
			}
			*v = w
			return false
		},
	}
}

// adjustMenuValue lowers or raises a menu row's value as a click on an
// increment/decrement toolbar button would, flashing the invalid cursor
// past the value's limits.
func adjustMenuValue[T ~int](ep *Scope, v *T, click MenuClickType, min, max, step int) {
	c := menuToolbarClick(click)
	if err := adjust(v, c, min, max, step); err != nil {
		ep.reportClickAtLimit(c)
	}
}

// menuToolbarClick returns the toolbar button click that a click on a menu
// row corresponds to.
func menuToolbarClick(click MenuClickType) toolbarClick {
	var c toolbarClick
	c.clicked[util.Select(click == MenuClickTertiary, platform.MouseButtonTertiary, platform.MouseButtonPrimary)] = true
	return c
}

// makeActionMenuItem builds a black row that performs an action when it is
// clicked.
func makeActionMenuItem(label string, onClick func(MenuClickType)) MenuItem {
	return MenuItem{
		Label:   label,
		BgColor: colors.popup.backgroundBlack,
		Color:   colors.popup.text,
		OnClick: func(click MenuClickType) bool {
			onClick(click)
			return false
		},
	}
}

// MenuConfig holds the full configuration for a menu drawn by DrawERAMMenu.
type MenuConfig struct {
	Title              string
	TitleLeftJustified bool // false = centered (default)
	ShowMButton        bool
	OnMClick           func(MenuClickType)
	Width              float32
	Font               *renderer.Font // row font; nil = ep.ERAMToolbarFont()
	TitleFont          *renderer.Font // title bar font; nil = same as Font
	ItemHeight         float32        // 0 = default 18px
	Rows               []MenuItem
}

// OpenPopupAt clamps originGuess so a menu with the given dimensions stays on
// screen — preferring to flip to the left of hostExtent when the popup would
// extend past the right edge of the pane — and warps the cursor to the center
// of the title-bar close (X) button, unless the menu then places it on a pick
// area (MenuItem.PlaceCursor). The returned placement carries the
// clamped origin plus the anchor side and pinned X that DrawView uses to
// keep the host view's pinned edge flush with the pop-up as the view
// resizes.
func (ep *Scope) OpenPopupAt(ctx *scope.Context, originGuess [2]float32, width, height float32, titleFont *renderer.Font, hostExtent math.Extent2D) viewPopupPlacement {
	pe := ctx.DrawExtent
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

// MenuResult is returned by DrawERAMMenu.
type MenuResult struct {
	Extent     math.Extent2D
	Dismissed  bool
	RowExtents []math.Extent2D
}

// DrawERAMMenu renders a floating popup menu and handles clicks.
func (ep *Scope) DrawERAMMenu(ctx *scope.Context, transforms scope.Transformations,
	cb *renderer.CommandBuffer, origin [2]float32, cfg MenuConfig) MenuResult {

	var result MenuResult

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
	var cursorTarget util.Optional[[2]float32]

	for i, item := range cfg.Rows {
		if item.SubRows != nil {
			// Scrollable list section
			cursor = ep.drawScrollSection(cursor, width, itemH, font, &cfg.Rows[i], trid, ld, td, mouse, &result.RowExtents[i])
		} else if item.Cells != nil {
			// Multi-cell pick-area row
			cursor = ep.drawMenuCellsRow(cursor, width, itemH, font, item.Cells, trid, ld, td, mouse, &result.RowExtents[i])
		} else if item.Grid != nil {
			// Scrollable value grid
			cursor = ep.drawMenuGrid(cursor, width, itemH, font, item.Grid, trid, ld, td, mouse, &result.RowExtents[i], &cursorTarget)
		} else if item.Input != nil {
			// Text input row
			cursor = ep.drawMenuInputRow(cursor, width, itemH, font, item.Input, trid, td, &result.RowExtents[i])
		} else if item.Custom != nil {
			h := item.Custom.Draw(cursor, width, &ViewBuilders{Trid: trid, Ld: ld, Td: td}, mouse)
			result.RowExtents[i] = math.Extent2D{P0: [2]float32{cursor[0], cursor[1] - h}, P1: [2]float32{cursor[0] + width, cursor[1]}}
			cursor[1] -= h
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

				clicked := ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse)
				clickType := util.Select(ep.mouseTertiaryClicked(mouse), MenuClickTertiary, MenuClickPrimary)
				x := rp0[0] + 4 + labelW
				for _, seg := range item.Segments {
					sext := font.InkBounds(seg.Text, 0)
					x0 := x + menuSegmentGap
					x1 := x0 + sext.Width() + 2*menuSegmentPad
					segColor := bText.ScaleRGB(seg.Color)
					if seg.Boxed {
						ld.AddLineLoop(segColor, [][2]float32{
							{x0, rp0[1] - itemH + 2}, {x1, rp0[1] - itemH + 2}, {x1, rp0[1] - 2}, {x0, rp0[1] - 2},
						})
					}
					td.AddText(seg.Text, [2]float32{x0 + menuSegmentPad - sext.P0[0], textY},
						renderer.TextStyle{Font: font, Color: segColor})

					segExt := math.Extent2D{P0: [2]float32{x0, rp0[1] - itemH}, P1: [2]float32{x1, rp0[1]}}
					if clicked && seg.OnClick != nil && segExt.Inside(mouse.Pos) && seg.OnClick(clickType) {
						ep.popup = nil
						result.Dismissed = true
					}
					x = x1
				}
			}

			cursor = rp3
			extent := math.Extent2D{P0: rp3, P1: rp1}
			result.RowExtents[i] = extent
			if item.PlaceCursor {
				cursorTarget.Set(extent.Center())
			}

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
	if cursorTarget.IsSet {
		ctx.SetMousePosition(cursorTarget.Value)
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
	// holding a BRIGHT-style row keeps incrementing. A press that began
	// elsewhere doesn't fire: the click that opened the menu may still be
	// held when the cursor is placed on a row.
	if mouse != nil {
		primaryDown := ep.mousePrimaryDown(mouse)
		tertiaryDown := ep.mouseTertiaryDown(mouse)
		clicked := ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse)
		if (primaryDown || tertiaryDown) && (clicked || ep.holdRepeat.fired) {
			hitRow := -1
			for i, ext := range result.RowExtents {
				if cfg.Rows[i].SubRows != nil || cfg.Rows[i].Cells != nil ||
					cfg.Rows[i].Grid != nil || cfg.Rows[i].Input != nil || cfg.Rows[i].Custom != nil {
					continue // these row types handle their own clicks (or take none)
				}
				if ext.Inside(mouse.Pos) {
					hitRow = i
					break
				}
			}
			if hitRow >= 0 {
				if ep.holdRepeat.fire(time.Now()) && cfg.Rows[hitRow].OnClick != nil {
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
func (ep *Scope) drawScrollSection(cursor [2]float32, width, itemH float32, font *renderer.Font,
	item *MenuItem, trid *renderer.ColoredTrianglesDrawBuilder, ld *renderer.ColoredLinesDrawBuilder,
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

// drawMenuCellsRow renders a row of side-by-side pick areas, splitting the
// row width by each cell's Weight. Cells handle their own clicks.
func (ep *Scope) drawMenuCellsRow(cursor [2]float32, width, itemH float32, font *renderer.Font,
	cells []MenuCell, trid *renderer.ColoredTrianglesDrawBuilder, ld *renderer.ColoredLinesDrawBuilder,
	td *renderer.TextDrawBuilder, mouse *platform.MouseState, extent *math.Extent2D) [2]float32 {

	ps := ep.currentPrefs()
	bButton := ps.Brightness.Button
	bBorder := ps.Brightness.Border
	bText := ps.Brightness.Text

	totalWeight := util.ReduceSlice(cells, 0, func(c MenuCell, w float32) float32 { return w + max(c.Weight, 1) })

	*extent = math.Extent2D{
		P0: [2]float32{cursor[0], cursor[1] - itemH},
		P1: [2]float32{cursor[0] + width, cursor[1]},
	}

	clicked := ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse)
	clickType := MenuClickPrimary
	if ep.mouseTertiaryClicked(mouse) {
		clickType = MenuClickTertiary
	}

	dimColor := bBorder.ScaleRGB(colors.menu.rowDimOutline)
	brightColor := bBorder.ScaleRGB(colors.menu.rowHoverOutline)

	x := cursor[0]
	var hovered *math.Extent2D
	for i := range cells {
		c := &cells[i]
		w := width * max(c.Weight, 1) / totalWeight
		cellExt := math.Extent2D{
			P0: [2]float32{x, cursor[1] - itemH},
			P1: [2]float32{x + w, cursor[1]},
		}

		trid.AddQuad([2]float32{x, cursor[1]}, [2]float32{x + w, cursor[1]},
			[2]float32{x + w, cursor[1] - itemH}, [2]float32{x, cursor[1] - itemH},
			bButton.ScaleRGB(c.BgColor))

		style := renderer.TextStyle{Font: font, Color: bText.ScaleRGB(c.Color)}
		td.AddTextCentered(c.Label, [2]float32{x + w/2, cursor[1] - itemH/2}, style)

		if mouse != nil && cellExt.Inside(mouse.Pos) {
			e := cellExt
			hovered = &e
			if clicked && c.OnClick != nil && c.OnClick(clickType) {
				ep.popup = nil
			}
		} else {
			ld.AddLineLoop(dimColor, cellExt.Corners())
		}
		x += w
	}
	// Hovered outline last so it overdraws the dim borders.
	if hovered != nil {
		ld.AddLineLoop(brightColor, hovered.Corners())
	}

	return [2]float32{cursor[0], cursor[1] - itemH}
}

// drawMenuGrid renders a scrollable grid of value pick areas with a reserved
// scroll-arrow column on the right if the rows don't all fit. Cells handle
// their own clicks. The center of a visible cell with PlaceCursor set is
// returned in cursorTarget.
func (ep *Scope) drawMenuGrid(cursor [2]float32, width, itemH float32, font *renderer.Font,
	grid *MenuGrid, trid *renderer.ColoredTrianglesDrawBuilder, ld *renderer.ColoredLinesDrawBuilder,
	td *renderer.TextDrawBuilder, mouse *platform.MouseState, extent *math.Extent2D,
	cursorTarget *util.Optional[[2]float32]) [2]float32 {

	ps := ep.currentPrefs()
	bButton := ps.Brightness.Button
	bText := ps.Brightness.Text

	visRows := grid.VisibleRows
	if visRows <= 0 {
		visRows = len(grid.Rows)
	}
	maxOffset := max(len(grid.Rows)-visRows, 0)
	*grid.Offset = math.Clamp(*grid.Offset, 0, maxOffset)

	scrolls := len(grid.Rows) > visRows
	contentW := width - util.Select(scrolls, float32(scrollBarTotalW+2), 0)
	sectionH := float32(visRows) * itemH

	// Section background
	bgP0 := cursor
	bgP1 := math.Add2f(bgP0, [2]float32{width, 0})
	bgP2 := math.Add2f(bgP1, [2]float32{0, -sectionH})
	bgP3 := math.Add2f(bgP0, [2]float32{0, -sectionH})
	trid.AddQuad(bgP0, bgP1, bgP2, bgP3, bButton.ScaleRGB(colors.popup.backgroundBlack))

	sectionExtent := math.Extent2D{P0: bgP3, P1: bgP1}
	*extent = sectionExtent

	clicked := ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse)
	clickType := MenuClickPrimary
	if ep.mouseTertiaryClicked(mouse) {
		clickType = MenuClickTertiary
	}

	bBorder := ps.Brightness.Border
	brightColor := bBorder.ScaleRGB(colors.menu.rowHoverOutline)
	var hovered *math.Extent2D

	for vi := range visRows {
		idx := *grid.Offset + vi
		if idx >= len(grid.Rows) {
			break
		}
		row := grid.Rows[idx]
		y := cursor[1] - float32(vi)*itemH

		totalWeight := util.ReduceSlice(row, 0, func(c MenuGridCell, w float32) float32 { return w + max(c.Weight, 1) })

		x := cursor[0]
		for ci := range row {
			c := &row[ci]
			w := contentW * max(c.Weight, 1) / totalWeight
			cellExt := math.Extent2D{
				P0: [2]float32{x, y - itemH},
				P1: [2]float32{x + w, y},
			}
			if c.PlaceCursor {
				cursorTarget.Set(cellExt.Center())
			}

			if c.BgColor != (renderer.RGB{}) {
				trid.AddQuad([2]float32{x, y}, [2]float32{x + w, y},
					[2]float32{x + w, y - itemH}, [2]float32{x, y - itemH},
					bButton.ScaleRGB(c.BgColor))
			}

			if c.Label != "" {
				style := renderer.TextStyle{Font: font, Color: bText.ScaleRGB(c.Color)}
				td.AddTextCentered(c.Label, [2]float32{x + w/2, y - itemH/2}, style)
			}

			if c.OnClick != nil && mouse != nil && cellExt.Inside(mouse.Pos) {
				e := cellExt
				if c.HoverSpansRow {
					e.P0[0] = cursor[0]
					e.P1[0] = cursor[0] + contentW
				}
				hovered = &e
				if clicked && c.OnClick(clickType) {
					ep.popup = nil
				}
			}
			x += w
		}
	}

	if hovered != nil {
		ld.AddLineLoop(brightColor, hovered.Corners())
	}
	if !scrolls {
		return bgP3
	}

	// Scroll arrows (right column): boxed up/down buttons in the same style
	// as the view scroll bars, scrolling page-sized steps like the real menus.
	canScrollUp := *grid.Offset > 0
	canScrollDown := *grid.Offset < maxOffset

	buttonH := (sectionH - 2 - scrollBarGap) / 2
	bx1 := cursor[0] + width - 1
	bx0 := bx1 - scrollBarTotalW
	upY1 := cursor[1] - 1
	upRect := math.Extent2D{P0: [2]float32{bx0, upY1 - buttonH}, P1: [2]float32{bx1, upY1}}
	downY0 := upY1 - buttonH - scrollBarGap
	downRect := math.Extent2D{P0: [2]float32{bx0, downY0 - buttonH}, P1: [2]float32{bx1, downY0}}

	for _, r := range [...]math.Extent2D{upRect, downRect} {
		addQuad(trid, r, colors.scroll.background)
		ld.AddLineLoop(colors.scroll.border, r.Corners())
	}
	arrowX := upRect.Center()[0]
	drawScrollArrow(ld, arrowX, upRect.P1[1]-1, -1, util.Select(canScrollUp, colors.scroll.arrow, colors.menu.scrollDimArrow))
	drawScrollArrow(ld, arrowX, downRect.P0[1]+1, +1, util.Select(canScrollDown, colors.scroll.arrow, colors.menu.scrollDimArrow))

	if mouse != nil && sectionExtent.Inside(mouse.Pos) {
		if mouse.Wheel[1] > 0 && canScrollUp {
			*grid.Offset--
		} else if mouse.Wheel[1] < 0 && canScrollDown {
			*grid.Offset++
		}
	}
	if clicked && mouse != nil {
		if upRect.Inside(mouse.Pos) && canScrollUp {
			*grid.Offset = max(*grid.Offset-visRows, 0)
		} else if downRect.Inside(mouse.Pos) && canScrollDown {
			*grid.Offset = min(*grid.Offset+visRows, maxOffset)
		}
	}

	return bgP3
}

// drawMenuInputRow renders a text input row: a black box showing the buffer
// contents with a trailing cursor. Keyboard input is the popup's concern.
func (ep *Scope) drawMenuInputRow(cursor [2]float32, width, itemH float32, font *renderer.Font,
	input *MenuInput, trid *renderer.ColoredTrianglesDrawBuilder,
	td *renderer.TextDrawBuilder, extent *math.Extent2D) [2]float32 {

	ps := ep.currentPrefs()
	bButton := ps.Brightness.Button
	bText := ps.Brightness.Text

	rp1 := math.Add2f(cursor, [2]float32{width, 0})
	rp2 := math.Add2f(rp1, [2]float32{0, -itemH})
	rp3 := math.Add2f(cursor, [2]float32{0, -itemH})
	trid.AddQuad(cursor, rp1, rp2, rp3, bButton.ScaleRGB(colors.popup.backgroundBlack))

	*extent = math.Extent2D{P0: rp3, P1: rp1}

	text := *input.Buf + "_"
	style := renderer.TextStyle{Font: font, Color: bText.ScaleRGB(colors.popup.text)}
	th := font.LayoutBounds(text, 0).Height()
	td.AddText(text, [2]float32{cursor[0] + 4, cursor[1] - itemH/2 + th/2}, style)

	return rp3
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

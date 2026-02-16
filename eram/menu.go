package eram

import (
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
)

// ERAMMenu - reusable floating popup menu component: streamlines making the many slightly different menus in ERAM.

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

// ERAMMenuConfig holds the full configuration for a menu drawn by DrawERAMMenu.
type ERAMMenuConfig struct {
	Title       string
	ShowMButton bool
	OnMClick    func(ERAMMenuClickType)
	OnClose     func()
	Width       float32
	Font        *renderer.Font // nil = ep.ERAMToolbarFont()
	ItemHeight  float32        // 0 = default 18px
	Rows        []ERAMMenuItem

	ShowBorder  bool
	BorderColor renderer.RGB

	DismissOnClickOutside bool

	// Escape hatch for custom drawn content (e.g. CRR color swatches).
	// Returns the new cursor position after drawing.
	CustomContent func(cursor [2]float32, width float32,
		trid *renderer.ColoredTrianglesDrawBuilder,
		ld *renderer.ColoredLinesDrawBuilder,
		td *renderer.TextDrawBuilder,
		mouse *platform.MouseState) [2]float32
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
	textColor := renderer.RGB{R: 1, G: 1, B: 1}

	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	cursor := origin

	// Title bar
	{
		rp0 := cursor
		rp1 := math.Add2f(cursor, [2]float32{width, 0})
		rp2 := math.Add2f(rp1, [2]float32{0, -itemH})
		rp3 := math.Add2f(rp0, [2]float32{0, -itemH})
		trid.AddQuad(rp0, rp1, rp2, rp3, eramGray)

		style := renderer.TextStyle{Font: font, Color: textColor}

		// Title text (centered)
		centerX := rp0[0] + width/2
		centerY := rp0[1] - itemH/2
		td.AddTextCentered(cfg.Title, [2]float32{centerX, centerY}, style)

		// Close button on right
		xLabel := "X"
		xw, _ := font.BoundText(xLabel, 0)
		xPos := math.Add2f(cursor, [2]float32{width - float32(xw) - 2, -1})
		td.AddText(xLabel, xPos, style)

		// Separator line at bottom of title
		ld.AddLine(rp0, rp1, eramGray.Scale(.25))
		// Left border of title
		ld.AddLine(cursor, [2]float32{cursor[0], rp3[1]}, renderer.RGB{})

		// Build extents for M button (optional) and X button
		var mRect, xRect, titleRect math.Extent2D

		if cfg.ShowMButton {
			mLabel := "M"
			mw, _ := font.BoundText(mLabel, 0)
			mPad := float32(2)
			mRect = math.Extent2D{
				P0: [2]float32{rp0[0], rp0[1] - itemH},
				P1: [2]float32{rp0[0] + mPad + float32(mw) + mPad, rp0[1]},
			}
			// draw M text
			_, mh := font.BoundText(mLabel, 0)
			td.AddText(mLabel, math.Add2f(rp0, [2]float32{mPad, -itemH/2 + float32(mh)/2}), style)
		}

		xPad := float32(2)
		xRect = math.Extent2D{
			P0: [2]float32{rp1[0] - xPad - float32(xw) - xPad, rp1[1] - itemH},
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

		// Draw non-hovered outlines first
		if cfg.ShowMButton && !mouseInsideM {
			drawMenuRectOutline(mRect, toolbarOutlineColor)
		}
		if !mouseInsideX {
			drawMenuRectOutline(xRect, toolbarOutlineColor)
		}
		if !mouseInsideTitle {
			drawMenuRectOutline(titleRect, toolbarOutlineColor)
		}
		// Draw hovered outlines last
		if cfg.ShowMButton && mouseInsideM {
			drawMenuRectOutline(mRect, toolbarHoveredOutlineColor)
		}
		if mouseInsideX {
			drawMenuRectOutline(xRect, toolbarHoveredOutlineColor)
		}
		if mouseInsideTitle {
			drawMenuRectOutline(titleRect, toolbarHoveredOutlineColor)
		}

		// Title bar click handling
		if ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse) {
			clickType := MenuClickPrimary
			if ep.mouseTertiaryClicked(mouse) {
				clickType = MenuClickTertiary
			}
			if mouseInsideX {
				if cfg.OnClose != nil {
					cfg.OnClose()
				}
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
			trid.AddQuad(rp0, rp1, rp2, rp3, item.BgColor)

			style := renderer.TextStyle{Font: font, Color: item.Color}
			if item.Centered {
				centerX := rp0[0] + width/2
				centerY := rp0[1] - itemH/2
				td.AddTextCentered(item.Label, [2]float32{centerX, centerY}, style)
			} else {
				td.AddText(item.Label, math.Add2f(rp0, [2]float32{4, -itemH + 12}), style)
			}

			cursor = rp3
			extent := math.Extent2D{P0: rp3, P1: rp1}
			result.RowExtents[i] = extent

			// Row hover outlines
			dimColor := eramGray.Scale(.25)
			brightColor := eramGray.Scale(.8)
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

	if cfg.ShowBorder {
		ld.AddLineLoop(cfg.BorderColor, [][2]float32{menuP0, menuP1, menuP2, menuP3})
	}

	// Flush draw builders
	transforms.LoadWindowViewingMatrices(cb)
	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)

	// Click-outside dismissal
	if cfg.DismissOnClickOutside {
		if ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse) {
			if mouse != nil && !result.Extent.Inside(mouse.Pos) {
				if cfg.OnClose != nil {
					cfg.OnClose()
				}
				result.Dismissed = true
			}
		}
	}

	// Row click handling (skip scroll rows - they handle their own clicks)
	if ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse) {
		clickType := MenuClickPrimary
		if ep.mouseTertiaryClicked(mouse) {
			clickType = MenuClickTertiary
		}
		for i, ext := range result.RowExtents {
			if cfg.Rows[i].SubRows != nil {
				continue // scroll rows handle clicks in drawScrollSection
			}
			if mouse != nil && ext.Inside(mouse.Pos) {
				if cfg.Rows[i].OnClick != nil {
					if cfg.Rows[i].OnClick(clickType) {
						result.Dismissed = true
					}
				}
				break
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
	trid.AddQuad(bgP0, bgP1, bgP2, bgP3, item.BgColor)

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
			trid.AddQuad(sp0, sp1, sp2, sp3, renderer.RGB{R: .3, G: .3, B: .6})
		}

		style := renderer.TextStyle{Font: font, Color: subItem.Color}
		td.AddText(subItem.Label, math.Add2f([2]float32{cursor[0], y}, [2]float32{4, -itemH + 12}), style)
	}

	// Scroll arrows (right side)
	canScrollUp := ss.Offset > 0
	canScrollDown := ss.Offset+visRows < len(item.SubRows)
	arrowColor := renderer.RGB{R: .7, G: .7, B: .7}
	dimArrowColor := renderer.RGB{R: .3, G: .3, B: .3}

	upCenter := [2]float32{cursor[0] + width - arrowW/2, cursor[1] - itemH/2}
	downCenter := [2]float32{cursor[0] + width - arrowW/2, cursor[1] - scrollH + itemH/2}

	upColor := dimArrowColor
	if canScrollUp {
		upColor = arrowColor
	}
	downColor := dimArrowColor
	if canScrollDown {
		downColor = arrowColor
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

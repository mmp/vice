// eram/drawview.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"slices"
	"strings"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
)

// DrawView renders ERAM floating windows (ALTIM SET, WX REPORT, MCA, RA, Clock,
// CRR) from a declarative View spec. It owns the common chrome — title bar
// with M / "-" buttons and hover outlines, window border, body background,
// scroll bar, drag-to-reposition, and click-consumption ordering — so callers
// only specify dimensions, decorations, and contents.

///////////////////////////////////////////////////////////////////////////
// Public types

// ViewBuilders is passed to a View's DrawBody callback. The draw builders are pooled by DrawView
// and flushed after DrawBody returns; DrawBody should not Generate* on them.
type ViewBuilders struct {
	Trid *renderer.ColoredTrianglesDrawBuilder
	Ld   *renderer.ColoredLinesDrawBuilder
	Td   *renderer.TextDrawBuilder
}

// ViewRepoState is the pane-wide drag-to-reposition state. Only one view can
// be repositioned at a time, so a single instance lives on ERAMPane and
// activeID identifies which view (by View.ID) currently owns the drag.
type ViewRepoState struct {
	activeID   string
	startTime  time.Time
	dragOffset [2]float32
}

// Cancel ends an in-progress drag.
func (s *ViewRepoState) Cancel() { s.activeID = "" }

// ViewScrollConfig describes the optional scroll bar on the right edge of the
// body. State is caller-owned so the view's body can read Offset when
// rendering. MaxOffset is the largest valid State.Offset; the scroll bar is
// shown when MaxOffset > 0 and clicks clamp Offset to [0, MaxOffset].
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
// pane-coord hit-test rectangle; Label is shown in the popup as "DELETE
// <Label>" and identifies the row. Labels among items in one frame are
// expected to be unique.
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
}

// View is the declarative spec. See package overview above.
type View struct {
	Position *[2]float32

	// ID identifies this view for the pane-wide drag state (ep.viewRepo).
	// Must be unique across all views and stable across frames.
	ID string

	// Width is the body *content* width. DrawView adds 2 space characters
	// (per BodyFont) of reserve on the right when Scroll is non-nil so the
	// scroll bar can be drawn without overlapping content.
	Width      float32
	BodyHeight float32

	Title     string
	TitleFont *renderer.Font

	// BodyFont is the font used for measuring body-related layout that
	// DrawView itself performs — currently just the scroll-bar reserve.
	// Falls back to TitleFont when nil.
	BodyFont *renderer.Font

	Opaque     bool
	ShowBorder bool
	Brightness radar.Brightness

	// OpaqueOnlyBg controls how the body background reacts to Opaque. When
	// false (default), the body background is always drawn black — used by
	// every multi-row view. When true, the body background follows Opaque:
	// grey (scaled by Brightness) when Opaque, not drawn at all when not
	// Opaque. Clock uses this so the scope shows through in T mode.
	OpaqueOnlyBg bool

	OnMenu func(host math.Extent2D) popup

	// MinimizeTarget enables the title-bar "-" button: when non-nil, clicking
	// "-" sets *MinimizeTarget = false.
	MinimizeTarget *bool

	OnBodyTertiaryMenu func(host math.Extent2D) popup

	Body func(extent math.Extent2D, b *ViewBuilders)

	Scroll     *ViewScrollConfig
	Selectable *ViewSelectable

	// RowSource, when set, supplies a column-major list-of-rows body.
	// DrawView builds per-column layouts, computes Width / BodyHeight, and
	// wires Scroll / Selectable. Callers must NOT also set Width,
	// BodyHeight, Body, Scroll, or Selectable when RowSource is set.
	RowSource *ViewRowSource
}

// ViewRowSource declaratively describes a column-major list-of-rows body for
// a View. The View handles column layout, scroll, padding, badge geometry,
// side pads, and the default text color; callers describe only the row data,
// the per-column content width, and a handful of high-level flags.
type ViewRowSource struct {
	// Rows is the flat slice in display order. Distributed column-major
	// (first VisibleRows fill column 0, next VisibleRows fill column 1, …).
	// Row.Color is filled with the View's default text color where the row
	// leaves it zero.
	Rows []Row

	// FontSize selects ERAMFont(FontSize); clamped to [1,3].
	FontSize int

	// ContentChars is the per-column body content width in characters of
	// the selected (fixed-width) font.
	ContentChars int

	MaxCols     int
	VisibleRows int

	// BadgeColumn reserves the leftmost badge column matching the title-bar
	// M button. Implied by BadgesVisible.
	BadgeColumn bool

	// BadgesVisible draws the standard yellow fill in the badge column,
	// scaled by Brightness. Implies BadgeColumn.
	BadgesVisible bool

	RowSpacing RowSpacing

	// ScrollState, when non-nil, enables the scroll bar. MaxOffset is
	// computed as max(0, len(Rows) - VisibleRows*MaxCols).
	ScrollState *ViewScrollState

	// CenterColumnsInTitleBar widens the view to at least the title bar's
	// minimum width and horizontally centers the column block. Used by CODE.
	CenterColumnsInTitleBar bool

	// EmptyKeepsColumnWidth keeps width = column width (rather than
	// collapsing to 0) when Rows is empty. Used by ALTIM SET so the title
	// bar still renders at a reasonable size.
	EmptyKeepsColumnWidth bool

	// SelectedID marks the row whose ID matches for selection highlight.
	SelectedID string

	// SelectableState / SelectableOnDelete wire click-to-delete using row IDs.
	SelectableState    *ViewSelectionState
	SelectableOnDelete func(label string)

	// OnRowExtents, if non-nil, is called after each column's Draw with the
	// visible-row extents flattened in display order (col 0 rows, then col 1
	// rows, …) and the row index of the first visible row. Used by CRR
	// list mode to populate its own click-tracking maps.
	OnRowExtents func(firstVisible int, extents []math.Extent2D)
}

// RowSpacing selects the vertical padding rule for a ViewRowSource.
type RowSpacing int

const (
	// RowSpacingCompact is the default for one-line rows (ALTIM SET, CODE,
	// CRR): lineH/4 above and below the list, lineH/4 below each row.
	RowSpacingCompact RowSpacing = iota
	// RowSpacingAiry is for rows whose Body may wrap to multiple lines
	// (WX): lineH/2 above and below each row's content.
	RowSpacingAiry
)

// Badge is an optional indicator on the left of a row: filled box with border.
type Badge struct {
	Width  float32
	Height float32
	Pad    float32
	Fill   renderer.RGB
	Border renderer.RGB
}

// Row is one row in a RowList. The layout has three logical regions:
//
//	[ Badge ][ Label ]  [ Body... wraps to multiple lines if needed ]
//	                    [ ...continuation aligned with Label start ]
//
// If Centered is true, Label is centered across the row width and Badge/Body
// are ignored (used for group-header rows).
// If Body is empty, the row has just Badge + Label.
// If SpacerHeight > 0, the row renders nothing and just takes up that much
// vertical space (used for inter-group gaps in CRR list mode).
type Row struct {
	Badge *Badge
	Label string
	Body  string

	Centered bool

	// ID is the selection identifier matched against RowList.SelectedID. It
	// is independent of Label so rows whose visible text lives entirely in
	// Body can still participate in selection.
	ID string

	Color renderer.RGB

	SpacerHeight float32

	// AfterDraw is called after the row content is drawn, with the row's
	// full extent and the baseline position where the body text starts.
	// Used by callers to add per-row decorations (e.g. AltimSet's altimeter
	// underline).
	AfterDraw func(extent math.Extent2D, bodyOrigin [2]float32, b *ViewBuilders)
}

///////////////////////////////////////////////////////////////////////////
// Layout constants and small helpers

const (
	scrollBarContentW = float32(14)
	scrollBarBorderW  = float32(1)
	scrollBarTotalW   = scrollBarContentW + 2*scrollBarBorderW
	scrollBarGap      = float32(2)

	// viewMPad is the horizontal padding on each side of the title-bar M
	// and "-" buttons, and the per-row side pad in the body.
	viewMPad = float32(4)

	// rowBadgeGap is the gap between a row's badge and the start of its
	// wrap area (used by the body-wrap calculation).
	rowBadgeGap = float32(4)
)

// addQuad fills extent ex with color using two triangles.
func addQuad(trid *renderer.ColoredTrianglesDrawBuilder, ex math.Extent2D, color renderer.RGB) {
	trid.AddQuad(
		ex.P0,                          // BL
		[2]float32{ex.P1[0], ex.P0[1]}, // BR
		ex.P1,                          // TR
		[2]float32{ex.P0[0], ex.P1[1]}, // TL
		color)
}

// addRectLoop draws a 1 px line loop along the perimeter of ex.
func addRectLoop(ld *renderer.ColoredLinesDrawBuilder, ex math.Extent2D, color renderer.RGB) {
	ld.AddLineLoop(color, [][2]float32{
		ex.P0,
		{ex.P1[0], ex.P0[1]},
		ex.P1,
		{ex.P0[0], ex.P1[1]},
	})
}

// drawRectOutline draws a 1 px line loop 1 px outside ex (used for title-bar
// button hover outlines).
func drawRectOutline(ld *renderer.ColoredLinesDrawBuilder, ex math.Extent2D, color renderer.RGB) {
	addRectLoop(ld, ex.Expand(1), color)
}

// deleteByID removes the first element of *sl whose key equals id.
// ViewSelectable.OnDelete callbacks for list views all follow this pattern.
func deleteByID[T any](sl *[]T, id string, idFn func(T) string) {
	for i, v := range *sl {
		if idFn(v) == id {
			*sl = slices.Delete(*sl, i, i+1)
			return
		}
	}
}

// clampViewPos clamps a view top-left so the whole window stays inside the
// pane, leaving the top-toolbar buffer free when the toolbar is visible.
// pos is the view's top-left in pane-local coords (y up).
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

// charWidth is the per-character advance width for a fixed-width font.
// All ERAM text fonts are fixed-width, so this is the same width for any
// printable ASCII character (M, -, 0, space, …).
func charWidth(font *renderer.Font) float32 {
	return font.LayoutBounds(" ", 0).Width()
}

// cellHeight is the font's cell height (one rendered line of text).
func cellHeight(font *renderer.Font) float32 {
	return font.LayoutBounds("0", 0).Height()
}

// lineHeight is the row line height ERAM views use: the font cell height
// plus a 2 px inter-row gap.
func lineHeight(font *renderer.Font) float32 {
	return cellHeight(font) + 2
}

// viewMButtonWidth returns the on-screen width of the title-bar M button.
// Callers that want a body element to line up directly below the M (badges,
// indicators) should size it with this.
func viewMButtonWidth(titleFont *renderer.Font) float32 {
	return charWidth(titleFont) + 2*viewMPad
}

// viewTitleHeight returns the on-screen height of the title bar.
func viewTitleHeight(titleFont *renderer.Font) float32 {
	return max(16, cellHeight(titleFont)+4)
}

// titleBarMinWidth is the minimum width the title bar needs to fit the M
// button on the left, the centered Title text, and the "-" button on the
// right without overlap.
func titleBarMinWidth(titleFont *renderer.Font, title string) float32 {
	cw := charWidth(titleFont)
	return float32(len(title))*cw + 2*(cw+2*viewMPad)
}

// defaultBadge builds the standard row badge: width and height matched to
// the title-bar M button, with the given fill and the default border color.
func defaultBadge(titleFont *renderer.Font, fill renderer.RGB) *Badge {
	return &Badge{
		Width:  charWidth(titleFont),
		Height: viewTitleHeight(titleFont),
		Pad:    viewMPad,
		Fill:   fill,
		Border: colors.badge.border,
	}
}

// scrollReserveWidth is the right-edge area DrawView reserves for the scroll
// bar when Scroll is configured: 2 BodyFont spaces, but at least
// scrollBarTotalW.
func scrollReserveWidth(v View) float32 {
	if v.Scroll == nil {
		return 0
	}
	font := v.BodyFont
	if font == nil {
		font = v.TitleFont
	}
	return max(2*charWidth(font), scrollBarTotalW)
}

// viewTextColor returns the standard list-view text color scaled by the
// given Brightness.
func (ep *ERAMPane) viewTextColor(b radar.Brightness) renderer.RGB {
	return b.ScaleRGB(colors.view.text)
}

///////////////////////////////////////////////////////////////////////////
// Title bar layout

// titleButton is one of the three clickable title-bar regions (M, title, "-").
type titleButton struct {
	rect    math.Extent2D
	present bool
	hovered bool
	label   string // "" for the title region (drawn separately, centered on full bar)
}

// titleBarLayout splits titleExt into the M / title / "-" regions and fills
// in hover flags. Returned in left-to-right order: [M, title, "-"].
func titleBarLayout(titleExt math.Extent2D, v View, mouse *platform.MouseState) [3]titleButton {
	titleH := titleExt.Height()
	var btns [3]titleButton

	btns[0].label = "M"
	btns[0].present = v.OnMenu != nil
	btns[0].rect = math.Extent2D{
		P0: titleExt.P0,
		P1: [2]float32{titleExt.P0[0] + viewMButtonWidth(v.TitleFont), titleExt.P1[1]},
	}

	btns[2].label = "-"
	btns[2].present = v.MinimizeTarget != nil
	btns[2].rect = math.Extent2D{
		P0: [2]float32{titleExt.P1[0] - 2*viewMPad - charWidth(v.TitleFont), titleExt.P1[1] - titleH},
		P1: titleExt.P1,
	}

	btns[1].present = true
	leftEdge := titleExt.P0[0]
	if btns[0].present {
		leftEdge = btns[0].rect.P1[0]
	}
	rightEdge := titleExt.P1[0]
	if btns[2].present {
		rightEdge = btns[2].rect.P0[0]
	}
	btns[1].rect = math.Extent2D{
		P0: [2]float32{leftEdge, titleExt.P0[1]},
		P1: [2]float32{rightEdge, titleExt.P1[1]},
	}

	for i := range btns {
		if btns[i].present && mouse != nil {
			btns[i].hovered = btns[i].rect.Inside(mouse.Pos)
		}
	}
	return btns
}

///////////////////////////////////////////////////////////////////////////
// Scroll bar

// scrollBarLayout returns the two clickable / drawable rectangles for the
// scroll-bar arrows, given the body extent. Used once by click handling and
// once by rendering so they stay in lock-step.
func scrollBarLayout(body math.Extent2D) (upRect, downRect math.Extent2D) {
	sectionH := (body.Height() - 2 - scrollBarGap) / 2
	x1 := body.P1[0] - 1
	x0 := x1 - scrollBarTotalW
	upY1 := body.P1[1] - 1
	upY0 := upY1 - sectionH
	downY0 := upY0 - scrollBarGap
	downY1 := downY0 - sectionH
	upRect = math.Extent2D{P0: [2]float32{x0, upY0}, P1: [2]float32{x1, upY1}}
	// downY1 < downY0 because y increases upward; canonical Extent2D wants P0 < P1.
	downRect = math.Extent2D{P0: [2]float32{x0, downY1}, P1: [2]float32{x1, downY0}}
	return
}

// drawScrollArrow renders a stair-stepped triangle whose tip is at (centerX,
// tipY) and whose 10-row body extends in the +y direction when dir = +1 or
// −y direction when dir = −1.
func drawScrollArrow(ld *renderer.ColoredLinesDrawBuilder, centerX, tipY float32, dir int, color renderer.RGB) {
	for i, w := range [...]float32{1, 1, 3, 3, 5, 5, 7, 7, 9, 9} {
		y := tipY + float32(dir*i)
		ld.AddLine([2]float32{centerX - w/2, y}, [2]float32{centerX + w/2, y}, color)
	}
}

///////////////////////////////////////////////////////////////////////////
// DrawView

// DrawView renders a View, processing chrome and clicks in this order:
// scroll bar → title-bar buttons → title-bar drag → body-tertiary menu →
// body-primary drag → finalize in-progress drag. Each handler consumes its
// click so Body sees only fall-through events.
func (ep *ERAMPane) DrawView(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer, v View) {
	mouse := ctx.Mouse

	// Defaults so callers can omit the obvious.
	if v.TitleFont == nil {
		v.TitleFont = ep.ERAMFont(2)
	}
	if v.RowSource != nil {
		ep.applyRowSource(&v)
	}

	// Total width includes the scroll-bar reserve on the right when scrolling.
	scrollReserveW := scrollReserveWidth(v)
	width := v.Width + scrollReserveW

	hasTitle := v.Title != ""
	var titleH float32
	if hasTitle {
		titleH = viewTitleHeight(v.TitleFont)
	}
	bodyH := v.BodyHeight
	totalH := titleH + bodyH

	// When this view's configuration pop-up is open, pin the view's edge
	// adjacent to the pop-up. The pop-up's origin is fixed at open time, so
	// width changes (font, columns, …) move the view's *other* edge.
	if vap, ok := ep.popup.(viewAnchoredPopup); ok {
		if id, side, pinX := vap.viewAnchor(); id == v.ID {
			switch side {
			case popupAnchorRight:
				(*v.Position)[0] = pinX - width
			case popupAnchorLeft:
				(*v.Position)[0] = pinX
			}
		}
	}

	// Clamp position on every frame so a saved off-screen position can't
	// produce negative scissor boxes. Write the clamped value back so the
	// caller's storage tracks the constrained position.
	*v.Position = ep.clampViewPos(ctx, *v.Position, width, totalH)
	pos := *v.Position // view top-left in pane coords (y up).

	outer := math.Extent2D{
		P0: [2]float32{pos[0], pos[1] - totalH},
		P1: [2]float32{pos[0] + width, pos[1]},
	}
	bodyOuter := math.Extent2D{
		P0: outer.P0,
		P1: [2]float32{outer.P1[0], outer.P1[1] - titleH},
	}
	// bodyExtent is what Body sees and what body-area clicks check against
	// — it excludes the right-edge scroll-bar reserve.
	bodyExtent := math.Extent2D{
		P0: bodyOuter.P0,
		P1: [2]float32{bodyOuter.P1[0] - scrollReserveW, bodyOuter.P1[1]},
	}
	titleExt := math.Extent2D{
		P0: [2]float32{outer.P0[0], bodyOuter.P1[1]},
		P1: outer.P1,
	}

	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	// Body background and window border (skip when body is empty).
	if bodyH > 0 {
		drawBodyBg := true
		var bodyBg renderer.RGB
		if v.OpaqueOnlyBg {
			if v.Opaque {
				bodyBg = v.Brightness.ScaleRGB(colors.view.opaqueBackground)
			} else {
				drawBodyBg = false
			}
		}
		if drawBodyBg {
			addQuad(trid, bodyOuter, bodyBg)
		}
		if v.ShowBorder {
			addRectLoop(ld, outer, ep.currentPrefs().Brightness.Border.ScaleRGB(colors.view.border))
		}
	}

	// Title bar.
	var tb [3]titleButton
	if hasTitle {
		titleDim := v.Brightness.ScaleRGB(colors.view.text)
		bright := colors.view.hoveredOutline
		dim := colors.view.buttonOutline

		titleBg := renderer.RGB{}
		if v.Opaque {
			titleBg = colors.view.opaqueBackground
		}
		addQuad(trid, titleExt, titleBg)

		tb = titleBarLayout(titleExt, v, mouse)

		// Title text is centered on the full title-bar width so it stays
		// visually centered regardless of which side buttons are present.
		titleColor := titleDim
		if tb[1].hovered {
			titleColor = bright
		}
		td.AddTextCentered(v.Title, titleExt.Center(),
			renderer.TextStyle{Font: v.TitleFont, Color: titleColor})

		// M / "-" labels centered in their own rects.
		for _, b := range tb {
			if !b.present || b.label == "" {
				continue
			}
			c := titleDim
			if b.hovered {
				c = bright
			}
			td.AddTextCentered(b.label, b.rect.Center(),
				renderer.TextStyle{Font: v.TitleFont, Color: c})
		}

		// Dim outlines first, then bright outlines on hover — the bright
		// pass must overdraw dim in the 1-px overlap at region boundaries.
		for _, b := range tb {
			if b.present && !b.hovered {
				drawRectOutline(ld, b.rect, dim)
			}
		}
		for _, b := range tb {
			if b.present && b.hovered {
				drawRectOutline(ld, b.rect, bright)
			}
		}
	}

	// Scroll-bar geometry up front so click handling and rendering agree.
	scrollVisible := v.Scroll != nil && v.Scroll.MaxOffset > 0 && bodyH > 0
	var scrollUpRect, scrollDownRect math.Extent2D
	if scrollVisible {
		scrollUpRect, scrollDownRect = scrollBarLayout(bodyOuter)
	}

	// Selectable rows: compute extents up front so click handling and the
	// hover-outline pass agree. Also forget any stale selection if the
	// delete popup is no longer the active popup (Escape, click-outside, or
	// another popup replacing it).
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

	// Click handling. Each branch consumes its click so Body sees only
	// fall-through events.
	primaryClicked := ep.mousePrimaryClicked(mouse)
	tertiaryClicked := ep.mouseTertiaryClicked(mouse)
	if mouse != nil && (primaryClicked || tertiaryClicked) {
		startDrag := func() {
			ep.viewRepo.activeID = v.ID
			ep.viewRepo.startTime = time.Now()
			ep.viewRepo.dragOffset = math.Sub2f(mouse.Pos, pos)
		}
		switch {
		case scrollVisible && scrollUpRect.Inside(mouse.Pos):
			if v.Scroll.State.Offset > 0 {
				v.Scroll.State.Offset--
			}
			ep.consumeMouseClick(mouse)

		case scrollVisible && scrollDownRect.Inside(mouse.Pos):
			if v.Scroll.State.Offset < v.Scroll.MaxOffset {
				v.Scroll.State.Offset++
			}
			ep.consumeMouseClick(mouse)

		case hasTitle && v.OnMenu != nil && tb[0].hovered:
			ep.popup = v.OnMenu(outer)
			ep.consumeMouseClick(mouse)

		case hasTitle && v.MinimizeTarget != nil && tb[2].hovered:
			*v.MinimizeTarget = false
			ep.consumeMouseClick(mouse)

		case hasTitle && tb[1].hovered && ep.viewRepo.activeID == "":
			startDrag()
			ep.consumeMouseClick(mouse)

		case selectableActive && primaryClicked && hoveredSelIdx >= 0:
			item := selItems[hoveredSelIdx]
			v.Selectable.State.Selected = item.Label
			ep.popup = ep.openDeleteEntryPopup(ctx, item, v.Selectable, v.TitleFont)
			ep.clearMousePrimaryConsumed(mouse)

		case !hasTitle && v.OnBodyTertiaryMenu != nil && tertiaryClicked &&
			ep.viewRepo.activeID == "" && bodyExtent.Inside(mouse.Pos):
			ep.popup = v.OnBodyTertiaryMenu(outer)
			ep.clearMouseTertiaryConsumed(mouse)

		case !hasTitle && primaryClicked && bodyExtent.Inside(mouse.Pos) && ep.viewRepo.activeID == "":
			startDrag()
			ep.clearMousePrimaryConsumed(mouse)

		case ep.viewRepo.activeID == v.ID && time.Since(ep.viewRepo.startTime) > 100*time.Millisecond:
			*v.Position = ep.clampViewPos(ctx, math.Sub2f(mouse.Pos, ep.viewRepo.dragOffset), width, totalH)
			ep.viewRepo.activeID = ""
			ep.consumeMouseClick(mouse)
		}
	}

	// Reposition preview outline. When the cursor leaves the pane ctx.Mouse
	// goes nil — but the drag is still in progress, so query the platform
	// directly and convert into pane-local coords. clampViewPos pins the
	// preview to the closest valid edge.
	if ep.viewRepo.activeID == v.ID {
		mousePos := [2]float32{}
		if mouse != nil {
			mousePos = mouse.Pos
		} else {
			mousePos = ctx.WindowToPane(ctx.Platform.GetMouse().Pos)
		}
		previewTL := ep.clampViewPos(ctx, math.Sub2f(mousePos, ep.viewRepo.dragOffset), width, totalH)
		preview := math.Extent2D{
			P0: [2]float32{previewTL[0], previewTL[1] - totalH},
			P1: [2]float32{previewTL[0] + width, previewTL[1]},
		}
		addRectLoop(ld, preview, colors.view.hoveredOutline)
	}

	// Body content.
	if v.Body != nil && bodyH > 0 {
		v.Body(bodyExtent, &ViewBuilders{Trid: trid, Ld: ld, Td: td})
	}

	// Hover outline for selectable rows. Skip when the row is also selected
	// — its highlight fill carries the visual instead.
	if selectableActive && hoveredSelIdx >= 0 {
		hovered := selItems[hoveredSelIdx]
		if state := v.Selectable.State; state == nil || hovered.Label != state.Selected {
			drawRectOutline(ld, hovered.Extent, colors.view.hoveredOutline)
		}
	}

	// Scroll-bar visual (drawn after Body so it sits on top of body content).
	if scrollVisible {
		for _, r := range [...]math.Extent2D{scrollUpRect, scrollDownRect} {
			addQuad(trid, r, colors.scroll.background)
			addRectLoop(ld, r, colors.scroll.border)
		}
		centerX := scrollUpRect.Center()[0]
		// Up arrow: tip 1 px below top, body extends downward (-y).
		drawScrollArrow(ld, centerX, scrollUpRect.P1[1]-1, -1, colors.scroll.arrow)
		// Down arrow: tip 1 px above bottom, body extends upward (+y).
		drawScrollArrow(ld, centerX, scrollDownRect.P0[1]+1, +1, colors.scroll.arrow)
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

///////////////////////////////////////////////////////////////////////////
// RowSource expansion

// applyRowSource fills in Width, BodyHeight, BodyFont, Body, Scroll, and
// Selectable on v from v.RowSource so the rest of DrawView can treat the
// view as if the caller had wired those fields up directly.
func (ep *ERAMPane) applyRowSource(v *View) {
	rs := v.RowSource

	font := ep.ERAMFont(rs.FontSize)
	lineH := lineHeight(font)
	textColor := v.Brightness.ScaleRGB(colors.view.text)

	// Reserve a badge column on the left when requested (BadgesVisible
	// implies BadgeColumn). The badge sits flush with body.P0 so it lines
	// up with the title-bar M button.
	badgeColumn := rs.BadgeColumn || rs.BadgesVisible
	leftChrome := viewMPad
	if badgeColumn {
		leftChrome = viewMButtonWidth(v.TitleFont)
	}
	colWidth := leftChrome + float32(rs.ContentChars)*charWidth(font) + viewMPad

	// Standard yellow badge, built once and shared by every row.
	var badge *Badge
	if rs.BadgesVisible {
		badge = defaultBadge(v.TitleFont, v.Brightness.ScaleRGB(colors.badge.fill))
	}

	// Prototype RowList copied per column (each column needs its own Rows
	// slice). All other fields are shared.
	proto := RowList{
		Font:       font,
		LineHeight: lineH,
		Width:      colWidth,
		MaxLines:   rs.VisibleRows,
		SelectedID: rs.SelectedID,
	}
	switch rs.RowSpacing {
	case RowSpacingCompact:
		proto.ListTopPad = lineH / 4
		proto.ListBottomPad = lineH / 4
		proto.BottomGap = lineH / 4
	case RowSpacingAiry:
		proto.TopPad = lineH / 2
		proto.BottomGap = lineH / 2
	}

	// Clamp the persisted scroll offset to the current row count.
	maxPerPage := rs.VisibleRows * rs.MaxCols
	maxOffset := max(0, len(rs.Rows)-maxPerPage)
	startIdx := 0
	if rs.ScrollState != nil {
		rs.ScrollState.Offset = math.Clamp(rs.ScrollState.Offset, 0, maxOffset)
		startIdx = rs.ScrollState.Offset
	}

	// Allocate only as many columns as we'll actually populate.
	remaining := len(rs.Rows) - startIdx
	actualCols := 1
	if remaining > 0 {
		actualCols = math.Clamp((remaining+rs.VisibleRows-1)/rs.VisibleRows, 1, rs.MaxCols)
	}
	cols := make([]*RowList, actualCols)
	for c := range cols {
		rl := proto
		cols[c] = &rl
	}
	for i := range remaining {
		c := i / rs.VisibleRows
		if c >= actualCols {
			break
		}
		r := rs.Rows[startIdx+i]
		// Default the row's text color and badge so simple callers can
		// leave these zero on Row.
		if r.Color == (renderer.RGB{}) {
			r.Color = textColor
		}
		if badge != nil && r.Badge == nil && !r.Centered && r.SpacerHeight == 0 {
			r.Badge = badge
		}
		cols[c].Rows = append(cols[c].Rows, r)
	}

	bodyHeight := float32(0)
	for _, col := range cols {
		if h := col.Measure(); h > bodyHeight {
			bodyHeight = h
		}
	}

	// View width: columns side-by-side, optionally widened to fit the title
	// bar with the columns centered (CODE).
	colsTotalW := float32(actualCols) * colWidth
	width := colsTotalW
	colsStart := float32(0)
	if rs.CenterColumnsInTitleBar {
		minW := titleBarMinWidth(v.TitleFont, v.Title)
		width = max(minW, colsTotalW)
		colsStart = (width - colsTotalW) / 2
	}
	if len(rs.Rows) == 0 && rs.EmptyKeepsColumnWidth {
		width = colWidth
		bodyHeight = 0
	}

	v.Width = width
	v.BodyHeight = bodyHeight
	if v.BodyFont == nil {
		v.BodyFont = font
	}

	colExt := func(c int, body math.Extent2D) math.Extent2D {
		x0 := body.P0[0] + colsStart + float32(c)*colWidth
		return math.Extent2D{
			P0: [2]float32{x0, body.P0[1]},
			P1: [2]float32{x0 + colWidth, body.P1[1]},
		}
	}

	v.Body = func(body math.Extent2D, b *ViewBuilders) {
		var allExtents []math.Extent2D
		for c, col := range cols {
			ext := col.Draw(colExt(c, body), b)
			if rs.OnRowExtents != nil {
				allExtents = append(allExtents, ext...)
			}
		}
		if rs.OnRowExtents != nil {
			rs.OnRowExtents(startIdx, allExtents)
		}
	}

	if rs.ScrollState != nil {
		v.Scroll = &ViewScrollConfig{State: rs.ScrollState, MaxOffset: maxOffset}
	}

	if rs.SelectableState != nil {
		v.Selectable = &ViewSelectable{
			State:    rs.SelectableState,
			OnDelete: rs.SelectableOnDelete,
			Items: func(body math.Extent2D) []ViewSelectableItem {
				var items []ViewSelectableItem
				for c, col := range cols {
					for i, e := range col.TextExtents(colExt(c, body)) {
						items = append(items, ViewSelectableItem{Extent: e, Label: col.Rows[i].ID})
					}
				}
				return items
			},
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// RowList — vertical-stack layout helper for a View body

// RowList is package-internal. applyRowSource constructs one per column,
// calls Measure to learn the body height, then Draw / TextExtents during
// the View's Body callback.
type RowList struct {
	Font       *renderer.Font
	LineHeight float32

	TopPad    float32 // above each row's content
	BottomGap float32 // below each row's content

	// List-level padding applied once at the top/bottom of the whole list
	// (in addition to per-row TopPad/BottomGap).
	ListTopPad    float32
	ListBottomPad float32

	// Width is the wrap width for body text and is also the assumed render
	// width — Draw uses the body extent for placement but expects its width
	// to match this value.
	Width float32

	Rows []Row

	// MaxLines caps the total wrapped body lines rendered. The last visible
	// row is truncated mid-content if needed; subsequent rows are dropped.
	// 0 = unlimited.
	MaxLines int

	// Selection: the visible row whose ID matches SelectedID gets the popup
	// grey fill and inverted text color.
	SelectedID string

	// Populated by Measure.
	measured     []measuredRow
	visibleCount int
}

type measuredRow struct {
	lines  []string // wrapped body; empty if Centered or no Body
	height float32
}

// labelGapChars is the gap between a row's label and the start of its body
// text, in font characters.
const labelGapChars = 3

// bodyAvailChars returns the body wrap widths in characters for the first
// line (after badge + label + label gap) and continuation lines (after badge
// only). ERAM fonts are fixed-width, so char count is exact.
func (l *RowList) bodyAvailChars(r Row) (firstChars, contChars int) {
	cw := charWidth(l.Font)
	leadingPx := viewMPad
	if r.Badge != nil {
		leadingPx = r.Badge.Width + rowBadgeGap
	}
	contChars = int((l.Width - leadingPx - viewMPad) / cw)
	firstChars = contChars
	if r.Label != "" {
		firstChars -= len(r.Label) + labelGapChars
	}
	return
}

// Measure pre-wraps each row's body to fit Width and returns the height of
// the visible portion (after MaxLines truncation). Must be called before
// Draw / TextExtents.
func (l *RowList) Measure() float32 {
	l.measured = make([]measuredRow, len(l.Rows))
	for i, r := range l.Rows {
		if r.SpacerHeight > 0 {
			l.measured[i].height = r.SpacerHeight
			continue
		}
		nLines := 1
		if !r.Centered && r.Body != "" {
			firstChars, contChars := l.bodyAvailChars(r)
			lines := wrapWords(r.Body, firstChars, contChars)
			if len(lines) == 0 {
				lines = []string{r.Body}
			}
			l.measured[i].lines = lines
			nLines = len(lines)
		}
		l.measured[i].height = l.TopPad + float32(nLines)*l.LineHeight + l.BottomGap
	}

	// Truncate at MaxLines wrapped body lines (spacers are free).
	l.visibleCount = len(l.Rows)
	if l.MaxLines > 0 {
		used := 0
		for i, m := range l.measured {
			if l.Rows[i].SpacerHeight > 0 {
				continue
			}
			n := max(len(m.lines), 1)
			if used+n <= l.MaxLines {
				used += n
				continue
			}
			keep := l.MaxLines - used
			if keep <= 0 {
				l.visibleCount = i
				break
			}
			if len(m.lines) > keep {
				l.measured[i].lines = m.lines[:keep]
			}
			l.measured[i].height = l.TopPad + float32(keep)*l.LineHeight + l.BottomGap
			l.visibleCount = i + 1
			break
		}
	}

	h := l.ListTopPad + l.ListBottomPad
	for i := 0; i < l.visibleCount; i++ {
		h += l.measured[i].height
	}
	return h
}

// iterRows walks the visible rows top-down, computing each row's full
// extent (full body width × row height) and invoking fn.
func (l *RowList) iterRows(body math.Extent2D, fn func(i int, m measuredRow, ext math.Extent2D)) {
	rowTop := body.P1[1] - l.ListTopPad
	for i := 0; i < l.visibleCount; i++ {
		m := l.measured[i]
		rowBottom := rowTop - m.height
		fn(i, m, math.Extent2D{
			P0: [2]float32{body.P0[0], rowBottom},
			P1: [2]float32{body.P1[0], rowTop},
		})
		rowTop = rowBottom
	}
}

// rowContentLayout returns the x positions for a non-Centered, non-Spacer
// row: labelX is where the label / continuation body lines start; bodyX is
// where the first wrapped body line starts (after the label and label-gap,
// if any).
func (l *RowList) rowContentLayout(r Row, body math.Extent2D) (labelX, bodyX float32) {
	if r.Badge != nil {
		labelX = body.P0[0] + r.Badge.Width + 2*r.Badge.Pad
	} else {
		labelX = body.P0[0] + viewMPad
	}
	bodyX = labelX
	if r.Label != "" {
		bodyX += float32(len(r.Label)+labelGapChars) * charWidth(l.Font)
	}
	return
}

// forBodyTextBits invokes fn(string, position) for each rendered text span
// (label + each wrapped body line) in row r.
func (l *RowList) forBodyTextBits(r Row, m measuredRow, labelX, bodyX, baseY float32, fn func(s string, pos [2]float32)) {
	if r.Label != "" {
		fn(r.Label, [2]float32{labelX, baseY})
	}
	for j, line := range m.lines {
		lx := bodyX
		if j > 0 {
			lx = labelX
		}
		fn(line, [2]float32{lx, baseY - float32(j)*l.LineHeight})
	}
}

// inkUnionOf returns the bounding box of all the ink forBodyTextBits would
// render for row r, in window coords.
func (l *RowList) inkUnionOf(r Row, m measuredRow, labelX, bodyX, baseY float32) math.Extent2D {
	inkUnion := math.EmptyExtent2D()
	l.forBodyTextBits(r, m, labelX, bodyX, baseY, func(s string, pos [2]float32) {
		ink := l.Font.InkBounds(s, 0)
		if ink.IsEmpty() {
			return
		}
		inkUnion = math.Union(inkUnion, math.Add2f(pos, ink.P0))
		inkUnion = math.Union(inkUnion, math.Add2f(pos, ink.P1))
	})
	return inkUnion
}

// TextExtents returns one Extent2D per visible row: a tight box around the
// rendered text (expanded by 1 px) for normal rows, or the full row extent
// for spacers and centered headers. Used for selection / hover hit-testing.
func (l *RowList) TextExtents(body math.Extent2D) []math.Extent2D {
	if l.measured == nil {
		panic("RowList.TextExtents: Measure must be called first")
	}
	by := cellHeight(l.Font)
	lineSlack := (l.LineHeight - by) / 2
	extents := make([]math.Extent2D, l.visibleCount)
	l.iterRows(body, func(i int, m measuredRow, fullExt math.Extent2D) {
		r := l.Rows[i]
		if r.SpacerHeight > 0 || r.Centered {
			extents[i] = fullExt
			return
		}
		labelX, bodyX := l.rowContentLayout(r, body)
		baseY := fullExt.P1[1] - l.TopPad - lineSlack
		ink := l.inkUnionOf(r, m, labelX, bodyX, baseY)
		if ink.IsEmpty() {
			extents[i] = fullExt
		} else {
			extents[i] = ink.Expand(1)
		}
	})
	return extents
}

// Draw renders the visible rows into body and returns per-rendered-row
// extents (P0 = bottom-left, P1 = top-right) for click hit-testing.
// Measure must have been called first.
func (l *RowList) Draw(body math.Extent2D, b *ViewBuilders) []math.Extent2D {
	if l.measured == nil {
		panic("RowList.Draw: Measure must be called first")
	}
	by := cellHeight(l.Font)
	// lineSlack centers each text line in its line cell so the rendered
	// text has equal slack above and below.
	lineSlack := (l.LineHeight - by) / 2
	// Badge vertical offset from baseY to the "0" glyph's ink center —
	// at larger fonts the layout cell has noticeably more space below the
	// glyph than the badge does, so we align to the ink rather than the cell.
	inkExt := l.Font.InkBounds("0", 0)
	badgeYOffset := (inkExt.P0[1] + inkExt.P1[1]) / 2
	extents := make([]math.Extent2D, l.visibleCount)

	l.iterRows(body, func(i int, m measuredRow, rowExt math.Extent2D) {
		extents[i] = rowExt
		r := l.Rows[i]
		if r.SpacerHeight > 0 {
			return
		}
		contentTop := rowExt.P1[1] - l.TopPad
		style := renderer.TextStyle{Font: l.Font, Color: r.Color}

		if r.Centered {
			tw := float32(len(r.Label)) * charWidth(l.Font)
			x := body.P0[0] + (body.Width()-tw)/2
			y := contentTop - lineSlack
			b.Td.AddText(r.Label, [2]float32{x, y}, style)
			if r.AfterDraw != nil {
				r.AfterDraw(rowExt, [2]float32{x, y}, b)
			}
			return
		}

		baseY := contentTop - lineSlack
		if r.Badge != nil {
			drawBadge(b, r.Badge, body.P0[0], baseY+badgeYOffset)
		}
		labelX, bodyX := l.rowContentLayout(r, body)

		// Selection highlight is drawn behind the text, so compute the
		// tight ink box first and recolor style before the draw pass.
		if l.SelectedID != "" && r.ID == l.SelectedID {
			ink := l.inkUnionOf(r, m, labelX, bodyX, baseY)
			if !ink.IsEmpty() {
				addQuad(b.Trid, ink.Expand(1), colors.popup.backgroundGrey)
				style.Color = colors.popup.backgroundBlack
			}
		}

		l.forBodyTextBits(r, m, labelX, bodyX, baseY, func(s string, pos [2]float32) {
			b.Td.AddText(s, pos, style)
		})
		if r.AfterDraw != nil {
			r.AfterDraw(rowExt, [2]float32{bodyX, baseY}, b)
		}
	})
	return extents
}

// drawBadge renders the badge starting at startX, centered vertically on
// yCenter, with a 1 px-inset fill quad inside the border.
func drawBadge(b *ViewBuilders, badge *Badge, startX, yCenter float32) {
	x0 := startX + badge.Pad
	border := math.Extent2D{
		P0: [2]float32{x0, yCenter - badge.Height/2},
		P1: [2]float32{x0 + badge.Width, yCenter + badge.Height/2},
	}
	addQuad(b.Trid, border, badge.Border)
	addQuad(b.Trid, border.Expand(-1), badge.Fill)
}

// wrapWords word-wraps text so the first line fits firstChars and continuation
// lines fit contChars. ERAM uses fixed-width fonts, so char count == pixel
// width / charWidth. Splits on whitespace; runs of multiple spaces collapse
// to one space between words. ASCII only.
func wrapWords(text string, firstChars, contChars int) []string {
	var lines []string
	var cur string
	isFirst := true
	for word := range strings.FieldsSeq(text) {
		avail := firstChars
		if !isFirst {
			avail = contChars
		}
		test := cur
		if test != "" {
			test += " "
		}
		test += word
		if len(test) > avail && cur != "" {
			lines = append(lines, cur)
			cur = word
			isFirst = false
		} else {
			cur = test
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

///////////////////////////////////////////////////////////////////////////
// deleteEntryPopup — the "DELETE <label>" confirmation popup opened when the
// user clicks a selectable row in a View.

// deleteEntryPopup's owner is the selection state of the view that opened
// it — DrawView checks identity to know when to clear the row highlight
// after dismissal.
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
	width := float32(len(text))*charWidth(font) + 2*pad
	height := cellHeight(font) + pad

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

	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	extent := math.Extent2D{
		P0: [2]float32{d.origin[0], d.origin[1] - d.height},
		P1: [2]float32{d.origin[0] + d.width, d.origin[1]},
	}
	hovered := mouse != nil && extent.Inside(mouse.Pos)

	addQuad(trid, extent, ps.Brightness.Button.ScaleRGB(colors.popup.backgroundGrey))

	outlineColor := colors.menu.rowDimOutline
	if hovered {
		outlineColor = colors.menu.rowHoverOutline
	}
	addRectLoop(ld, extent, ps.Brightness.Border.ScaleRGB(outlineColor))

	td.AddTextCentered("DELETE "+d.label, extent.Center(),
		renderer.TextStyle{Font: d.font, Color: ps.Brightness.Text.ScaleRGB(colors.popup.text)})

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

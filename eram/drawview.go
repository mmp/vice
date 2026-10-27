// eram/drawview.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/util"
)

// DrawView renders the ERAM views from a declarative View spec. It owns the
// common chrome — title bar with M / "-" buttons and hover outlines, window
// border, body background, scroll bar, drag-to-reposition, and
// click-consumption ordering — so callers only specify dimensions,
// decorations, and contents.

///////////////////////////////////////////////////////////////////////////
// Public types

// ViewBuilders is passed to a View's DrawBody callback. The draw builders are pooled by DrawView
// and flushed after DrawBody returns; DrawBody should not Generate* on them.
type ViewBuilders struct {
	Trid *renderer.ColoredTrianglesDrawBuilder
	Ld   *renderer.ColoredLinesDrawBuilder
	Td   *renderer.TextDrawBuilder
}

// ViewRepoState is the scope-wide drag-to-reposition state. Only one view can
// be repositioned at a time, so a single instance lives on Scope and
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

// RowClick is a click on a row of a view, given to the row's OnClick.
type RowClick struct {
	View     string // the view's ID
	Idx      int    // the row's index into ViewRowSource.Rows
	Extent   math.Extent2D
	Pos      [2]float32
	Tertiary bool
}

// View is the declarative spec. See package overview above.
type View struct {
	// Prefs, if set, are the view's settings: DrawView takes its position,
	// opacity, border, and brightness from them, and the "-" button
	// suppresses the view unless OnMinimize is set. The views without the
	// standard settings set those fields themselves.
	Prefs *ViewPreferences

	Position *[2]float32

	// ID identifies this view for the pane-wide drag state (ep.viewRepo).
	// Must be unique across all views and stable across frames.
	ID string

	// Width is the body *content* width. DrawView adds 2 space characters
	// (per BodyFont) of reserve on the right when Scroll is non-nil so the
	// scroll bar can be drawn without overlapping content.
	Width      float32
	BodyHeight float32

	Title string

	// BodyFont is the font used for measuring body-related layout that
	// DrawView itself performs — currently just the scroll-bar reserve.
	// Falls back to the title font when nil.
	BodyFont *renderer.Font

	// Opaque views have a black body that hides what is behind them;
	// semi-transparent ones draw only their contents over the scope (4.1.5).
	Opaque     bool
	ShowBorder bool
	Brightness scope.Brightness

	// Menu is the view's menu, which the title bar's M button displays, or
	// a TBE in the body of a view without a title bar.
	Menu viewMenu

	// OnMinimize enables the title-bar "-" button: when non-nil, clicking
	// "-" invokes the callback. Callers typically set a "Visible" pref to
	// false (or to a hidden-sentinel value for enum-style visibility).
	OnMinimize func()

	// TitleButtons are more buttons in the title bar, laid out right to
	// left before the "-" button.
	TitleButtons []ViewTitleButton

	DrawBody func(extent math.Extent2D, b *ViewBuilders)

	// Scroll, when set, gives the body a scroll bar. applyRowSource sets it
	// for a RowSource view with a ScrollState.
	Scroll *ViewScrollConfig

	// RowSource, when set, supplies a column-major list-of-rows body.
	// DrawView builds per-column layouts, computes Width / BodyHeight, and
	// wires scroll and the rows' pick areas. Callers must NOT also set
	// Width, BodyHeight, or DrawBody when RowSource is set.
	RowSource *ViewRowSource

	// pickRows and tearOffBars are populated by applyRowSource (or left nil
	// for non-RowSource views, which don't use them).
	pickRows    func(body math.Extent2D) []rowPickArea
	tearOffBars func(body math.Extent2D) []tearOffBar
}

// rowPickArea is a row with an OnClick, as laid out in the body.
type rowPickArea struct {
	idx    int // into ViewRowSource.Rows
	extent math.Extent2D
	row    Row
}

// tearOffBar is a gold tear-off bar in a view, a pick on which starts
// tearing its entry off. origin is the top left of the entry's tear-off
// when it lies over the entry.
type tearOffBar struct {
	entry  viewEntry
	extent math.Extent2D
	origin [2]float32
}

// ViewTitleButton is a labeled button in a view's title bar, such as the
// CFR view's DEL ALL.
type ViewTitleButton struct {
	Label    string
	Disabled bool // grayed out, and clicks on it do nothing
	OnClick  func()
}

// ViewRowSource declaratively describes a column-major list-of-rows body for
// a View. The View handles column layout, scroll, padding, badge geometry,
// side pads, and the default text color; callers describe only the row data,
// the per-column content width, and a handful of high-level flags.
type ViewRowSource struct {
	// Rows is the flat slice in display order. Distributed column-major
	// (first VisibleRows fill column 0, next VisibleRows fill column 1, …),
	// with the last column taking the rest up to its VisibleRows lines.
	// Row.Color is filled with the View's default text color where the row
	// leaves it zero.
	Rows []Row

	// FontIndex selects ERAMFont(FontIndex); clamped to [1,3].
	FontIndex int

	// ContentChars is the per-column body content width in characters of
	// the selected (fixed-width) font.
	ContentChars int

	MaxCols int
	// VisibleRows is the number of lines a column shows. A row whose Body
	// wraps takes more than one.
	VisibleRows int

	// ReserveBadgeColumn reserves the leftmost badge column matching the
	// title-bar M button.
	ReserveBadgeColumn bool

	// TearOffs are the tear-offs of the view's entries. While
	// ShowTearOffBars is set, each row with a TearOff ID carries its
	// entry's tear-off bar: gold while the entry can be torn off and gray
	// once it has been (2.5.2).
	TearOffs        *entryTearOffs
	ShowTearOffBars bool

	RowSpacing RowSpacing

	// ScrollState, when non-nil, enables the scroll bar. Its Offset counts
	// lines, so that every line of a row that wraps can be scrolled into
	// view (4.2.20); MaxOffset is the number of lines past
	// VisibleRows*MaxCols.
	ScrollState *ViewScrollState

	// CenterColumnsInTitleBar widens the view to at least the title bar's
	// minimum width and horizontally centers the column block. Used by CODE.
	CenterColumnsInTitleBar bool

	// EmptyKeepsColumnWidth keeps width = column width (rather than
	// collapsing to 0) when Rows is empty. Used by ALTIM SET so the title
	// bar still renders at a reasonable size.
	EmptyKeepsColumnWidth bool

	// HighlightColor is the fill drawn behind rows with Row.Toggled=true.
	HighlightColor renderer.RGB
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
	Fill   renderer.RGB
	Border renderer.RGB
}

// Row is one row in a RowList. The layout has three logical regions:
//
//	[ Badge ][ Label ]  [ Body... wraps to multiple lines if needed ]
//	                    [ ...continuation aligned with Label start ]
//
// If Centered is true, Label is centered across the row width and Body is
// ignored (used for group-header rows).
// If Body is empty, the row has just Badge + Label.
// If SpacerHeight > 0, the row renders nothing and just takes up that much
// vertical space (used for inter-group gaps in CRR list mode).
type Row struct {
	Badge *Badge
	Label string
	Body  string

	Centered bool

	Color renderer.RGB

	// Toggled, when true, renders a RowList.HighlightColor fill behind the
	// row. Used by check list views for click-to-toggle highlighting.
	Toggled bool

	// Selected, when true, draws the row's text dark on a gray box, as a
	// picked entry is drawn.
	Selected bool

	SpacerHeight float32

	// TearOff is the ID of the list entry or sublist whose tear-off bar
	// the row carries; it is empty for the entry's other rows.
	TearOff string

	// OnClick, if set, makes the row a pick area: it takes the clicks on
	// the row, which is outlined while the cursor is over it.
	OnClick func(c RowClick)

	// AfterDraw is called after the row content is drawn, with the row's
	// full extent and the baseline position where the body text starts.
	// Used by callers to add per-row decorations (e.g. AltimSet's altimeter
	// underline).
	AfterDraw func(extent math.Extent2D, bodyOrigin [2]float32, b *ViewBuilders)
}

///////////////////////////////////////////////////////////////////////////
// Layout constants and small helpers

const (
	scrollBarContentW = 14
	scrollBarBorderW  = 1
	scrollBarTotalW   = scrollBarContentW + 2*scrollBarBorderW
	scrollBarGap      = 2

	// titleBarButtonPad is the horizontal padding on each side of the title-bar M
	// and "-" buttons, and the per-row side pad in the body.
	titleBarButtonPad = 4

	// rowBadgeGap is the gap between a row's badge and the start of its
	// wrap area (used by the body-wrap calculation).
	rowBadgeGap = 4
)

// addQuad fills extent ex with color using two triangles.
func addQuad(trid *renderer.ColoredTrianglesDrawBuilder, ex math.Extent2D, color renderer.RGB) {
	p := ex.Corners()
	trid.AddQuad(p[0], p[1], p[2], p[3], color)
}

// clampViewPos clamps a view top-left so the whole window stays inside the
// pane. pos is the view's top-left in pane-local coords (y up).
func (ep *Scope) clampViewPos(ctx *scope.Context, pos [2]float32, width, totalH float32) [2]float32 {
	paneW := ctx.DrawExtent.Width()
	paneH := ctx.DrawExtent.Height()
	pos[0] = math.Clamp(pos[0], 0, max(0, paneW-width))
	pos[1] = math.Clamp(pos[1], totalH, max(totalH, paneH))
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
	return charWidth(titleFont) + 2*titleBarButtonPad
}

// viewTitleHeight returns the on-screen height of the title bar.
func viewTitleHeight(titleFont *renderer.Font) float32 {
	return max(16, cellHeight(titleFont)+4)
}

// titleBarMinWidth is the minimum width the title bar needs to fit the M
// button on the left, the centered Title text, and the "-" button and any
// others on the right without overlap.
func titleBarMinWidth(titleFont *renderer.Font, title string, buttons []ViewTitleButton) float32 {
	cw := charWidth(titleFont)
	return util.ReduceSlice(buttons, float32(len(title))*cw+2*(cw+2*titleBarButtonPad),
		func(b ViewTitleButton, w float32) float32 { return w + float32(len(b.Label))*cw + 2*titleBarButtonPad })
}

// tearOffBadge builds a tear-off bar, sized to match the title-bar M
// button: gold if its entry can be torn off and gray if it has been
// (2.5.2).
func tearOffBadge(titleFont *renderer.Font, b scope.Brightness, torn bool) *Badge {
	return &Badge{
		Width:  charWidth(titleFont),
		Height: viewTitleHeight(titleFont),
		Fill:   b.ScaleRGB(util.Select(torn, colors.badge.torn, colors.badge.fill)),
		Border: colors.badge.border,
	}
}

// scrollReserveWidth is the right-edge area DrawView reserves for the scroll
// bar when Scroll is configured: 2 BodyFont spaces, but at least
// scrollBarTotalW.
func scrollReserveWidth(v View, titleFont *renderer.Font) float32 {
	if v.Scroll == nil {
		return 0
	}
	font := v.BodyFont
	if font == nil {
		font = titleFont
	}
	return max(2*charWidth(font), scrollBarTotalW)
}

// viewTextColor returns the standard list-view text color scaled by the
// given Brightness.
func (ep *Scope) viewTextColor(b scope.Brightness) renderer.RGB {
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

// titleBarLayout splits titleExt into the M / title / "-" regions and the
// regions of the view's TitleButtons, and fills in hover flags. The first
// are returned in left-to-right order: [M, title, "-"]; the TitleButtons'
// are in their order.
func titleBarLayout(titleExt math.Extent2D, v View, titleFont *renderer.Font, mouse *platform.MouseState) ([3]titleButton, []titleButton) {
	titleH := titleExt.Height()
	var btns [3]titleButton

	btns[0].label = "M"
	btns[0].present = v.Menu != nil
	btns[0].rect = math.Extent2D{
		P0: titleExt.P0,
		P1: [2]float32{titleExt.P0[0] + viewMButtonWidth(titleFont), titleExt.P1[1]},
	}

	btns[2].label = "-"
	btns[2].present = v.OnMinimize != nil
	btns[2].rect = math.Extent2D{
		P0: [2]float32{titleExt.P1[0] - 2*titleBarButtonPad - charWidth(titleFont), titleExt.P1[1] - titleH},
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
	extras := make([]titleButton, len(v.TitleButtons))
	for i, b := range v.TitleButtons {
		w := float32(len(b.Label))*charWidth(titleFont) + 2*titleBarButtonPad
		extras[i] = titleButton{present: true, label: b.Label, rect: math.Extent2D{
			P0: [2]float32{rightEdge - w, titleExt.P0[1]},
			P1: [2]float32{rightEdge, titleExt.P1[1]},
		}}
		rightEdge -= w
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
	for i := range extras {
		extras[i].hovered = mouse != nil && !v.TitleButtons[i].Disabled && extras[i].rect.Inside(mouse.Pos)
	}
	return btns, extras
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
// scroll bar → title-bar buttons → title-bar drag → tear-off bars → rows
// with pick areas → body-tertiary menu → body-primary drag → finalize
// in-progress drag. Each handler consumes its click so Body sees only
// fall-through events.
func (ep *Scope) DrawView(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer, v View) {
	mouse := ctx.Mouse

	titleFont := ep.ERAMFont(2)
	ep.resolveView(&v, titleFont)
	ep.drawnViews = append(ep.drawnViews, v.ID)

	// Total width includes the scroll-bar reserve on the right when scrolling.
	scrollReserveW := scrollReserveWidth(v, titleFont)
	width := v.Width + scrollReserveW

	hasTitle := v.Title != ""
	var titleH float32
	if hasTitle {
		titleH = viewTitleHeight(titleFont)
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
	// produce negative scissor boxes. The clamped position isn't stored, so
	// a view that grows past the edge of the pane moves back once it
	// shrinks again.
	pos := ep.clampViewPos(ctx, *v.Position, width, totalH) // view top-left in pane coords (y up).

	outer := math.Extent2D{
		P0: [2]float32{pos[0], pos[1] - totalH},
		P1: [2]float32{pos[0] + width, pos[1]},
	}
	ep.viewExtents = append(ep.viewExtents, outer)
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
		if v.Opaque {
			addQuad(trid, bodyOuter, renderer.RGB{})
		}
		if v.ShowBorder {
			ld.AddLineLoop(ep.currentPrefs().Brightness.Border.ScaleRGB(colors.view.border), outer.Corners())
		}
	}

	// Title bar.
	var tb [3]titleButton
	var tbExtras []titleButton
	if hasTitle {
		titleDim := v.Brightness.ScaleRGB(colors.view.text)
		bright := colors.view.hoveredOutline
		dim := colors.view.buttonOutline

		titleBg := renderer.RGB{}
		if v.Opaque {
			titleBg = colors.view.opaqueBackground
		}
		addQuad(trid, titleExt, titleBg)

		tb, tbExtras = titleBarLayout(titleExt, v, titleFont, mouse)

		// Title text is centered on the full title-bar width so it stays
		// visually centered regardless of which side buttons are present,
		// unless there are TitleButtons, which it would run into.
		titleColor := titleDim
		if tb[1].hovered {
			titleColor = bright
		}
		td.AddTextCentered(v.Title, util.Select(len(tbExtras) > 0, tb[1].rect, titleExt).Center(),
			renderer.TextStyle{Font: titleFont, Color: titleColor})

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
				renderer.TextStyle{Font: titleFont, Color: c})
		}

		// Dim chrome: horizontal title/body separator and single vertical
		// separators between adjacent title-bar buttons. The outer view
		// border covers the title bar's top/sides.
		ld.AddLine([2]float32{titleExt.P0[0], titleExt.P0[1]},
			[2]float32{titleExt.P1[0], titleExt.P0[1]}, dim)
		if tb[0].present {
			x := tb[0].rect.P1[0]
			ld.AddLine([2]float32{x, titleExt.P0[1]}, [2]float32{x, titleExt.P1[1]}, dim)
		}
		if tb[2].present {
			x := tb[2].rect.P0[0]
			ld.AddLine([2]float32{x, titleExt.P0[1]}, [2]float32{x, titleExt.P1[1]}, dim)
		}
		for i, b := range tbExtras {
			c := util.Select(b.hovered, bright, titleDim)
			if v.TitleButtons[i].Disabled {
				c = c.Scale(0.5)
			}
			td.AddTextCentered(b.label, b.rect.Center(), renderer.TextStyle{Font: titleFont, Color: c})
			x := b.rect.P0[0]
			ld.AddLine([2]float32{x, titleExt.P0[1]}, [2]float32{x, titleExt.P1[1]}, dim)
		}
		// Bright box around the hovered button. Overdraws the dim separators
		// (and the outer view border, where they coincide) so the hovered
		// cell reads as fully outlined.
		for _, b := range append(tb[:], tbExtras...) {
			if b.present && b.hovered {
				ld.AddLineLoop(bright, b.rect.Corners())
			}
		}
	}

	// Scroll-bar geometry up front so click handling and rendering agree.
	scrollVisible := v.Scroll != nil && v.Scroll.MaxOffset > 0 && bodyH > 0
	var scrollUpRect, scrollDownRect math.Extent2D
	if scrollVisible {
		scrollUpRect, scrollDownRect = scrollBarLayout(bodyOuter)
	}

	// The rows' pick areas and the tear-off bars, laid out up front so that
	// click handling and the hover outlines agree.
	var picks []rowPickArea
	hoveredPick := -1
	if v.pickRows != nil && bodyH > 0 {
		picks = v.pickRows(bodyExtent)
		if mouse != nil {
			hoveredPick = slices.IndexFunc(picks, func(a rowPickArea) bool { return a.extent.Inside(mouse.Pos) })
		}
	}
	var bars []tearOffBar
	hoveredBar := -1
	if v.tearOffBars != nil && bodyH > 0 {
		bars = v.tearOffBars(bodyExtent)
		if mouse != nil {
			hoveredBar = slices.IndexFunc(bars, func(b tearOffBar) bool { return b.extent.Inside(mouse.Pos) })
		}
	}

	// Click handling. Each branch consumes its click so Body sees only
	// fall-through events. Pop-ups always win clicks within their extent —
	// the popup is rendered on top, so a click in the overlap region belongs
	// to the popup, not the view underneath.
	primaryClicked := ep.mousePrimaryClicked(mouse)
	tertiaryClicked := ep.mouseTertiaryClicked(mouse)
	hoveredExtra := slices.IndexFunc(tbExtras, func(b titleButton) bool { return b.hovered })
	if mouse != nil && (primaryClicked || tertiaryClicked) && !ep.popupCovers(mouse.Pos) {
		// Starting a drag dismisses any open pop-up: the user is moving the
		// view, so the pop-up should no longer pin it (and our re-pinning
		// would otherwise snap it back to its old X on the next frame).
		startDrag := func() {
			ep.viewRepo.activeID = v.ID
			ep.viewRepo.startTime = time.Now()
			ep.viewRepo.dragOffset = math.Sub2f(mouse.Pos, pos)
			ep.popup = nil
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

		case hasTitle && v.Menu != nil && tb[0].hovered:
			ep.openViewMenu(ctx, v.ID, v.Menu, outer)
			ep.consumeMouseClick(mouse)

		case hasTitle && v.OnMinimize != nil && tb[2].hovered:
			v.OnMinimize()
			ep.consumeMouseClick(mouse)

		case hoveredExtra >= 0:
			v.TitleButtons[hoveredExtra].OnClick()
			ep.consumeMouseClick(mouse)

		case hasTitle && tb[1].hovered && ep.viewRepo.activeID == "":
			startDrag()
			ep.consumeMouseClick(mouse)

		case hoveredBar >= 0 && ep.viewRepo.activeID == "":
			ep.startEntryDrag(ctx, bars[hoveredBar].entry, bars[hoveredBar].origin)
			ep.consumeMouseClick(mouse)

		case hoveredPick >= 0:
			a := picks[hoveredPick]
			a.row.OnClick(RowClick{View: v.ID, Idx: a.idx, Extent: a.extent, Pos: mouse.Pos, Tertiary: tertiaryClicked})
			ep.consumeMouseClick(mouse)

		case !hasTitle && v.Menu != nil && tertiaryClicked &&
			ep.viewRepo.activeID == "" && bodyExtent.Inside(mouse.Pos):
			ep.openViewMenu(ctx, v.ID, v.Menu, outer)
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
			mousePos = ctx.WindowToDraw(ctx.Platform.GetMouse().Pos)
		}
		previewTL := ep.clampViewPos(ctx, math.Sub2f(mousePos, ep.viewRepo.dragOffset), width, totalH)
		preview := math.Extent2D{
			P0: [2]float32{previewTL[0], previewTL[1] - totalH},
			P1: [2]float32{previewTL[0] + width, previewTL[1]},
		}
		ld.AddLineLoop(colors.view.hoveredOutline, preview.Corners())
	}

	// Body content.
	if v.DrawBody != nil && bodyH > 0 {
		v.DrawBody(bodyExtent, &ViewBuilders{Trid: trid, Ld: ld, Td: td})
	}

	if hoveredPick >= 0 {
		ld.AddLineLoop(colors.view.hoveredOutline, picks[hoveredPick].extent.Expand(1).Corners())
	}
	if hoveredBar >= 0 {
		ld.AddLineLoop(colors.view.hoveredOutline, bars[hoveredBar].extent.Expand(1).Corners())
	}

	// Scroll-bar visual (drawn after Body so it sits on top of body content).
	if scrollVisible {
		for _, r := range [...]math.Extent2D{scrollUpRect, scrollDownRect} {
			addQuad(trid, r, colors.scroll.background)
			ld.AddLineLoop(colors.scroll.border, r.Corners())
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

// resolveView fills in the fields of v that its Prefs and RowSource
// supply. Prefs come first: the rows' default text color and tear-off
// bars take the view's brightness.
func (ep *Scope) resolveView(v *View, titleFont *renderer.Font) {
	if prefs := v.Prefs; prefs != nil {
		v.Position = &prefs.Position
		v.Opaque, v.ShowBorder, v.Brightness = prefs.Opaque, prefs.ShowBorder, prefs.Bright
		if v.OnMinimize == nil {
			v.OnMinimize = func() { prefs.Visible = false }
		}
	}
	if v.RowSource != nil {
		ep.applyRowSource(v, titleFont)
	}
}

// applyRowSource fills in Width, BodyHeight, BodyFont, DrawBody, Scroll, and
// the rows' pick areas on v from v.RowSource so the rest of DrawView can
// treat the view as if the caller had wired those fields up directly.
func (ep *Scope) applyRowSource(v *View, titleFont *renderer.Font) {
	rs := v.RowSource

	if rs.VisibleRows < 1 || rs.MaxCols < 1 {
		panic(fmt.Sprintf("ViewRowSource %q: VisibleRows (%d) and MaxCols (%d) must both be at least 1; "+
			"a zero here usually means the view's preferences were never initialized",
			v.ID, rs.VisibleRows, rs.MaxCols))
	}

	// Prototype RowList copied per column (each column needs its own Rows
	// slice). All other fields are shared.
	proto := ep.rowColumn(rs, titleFont)
	colWidth := proto.Width

	// Default the rows' text color and tear-off bars so simple callers can
	// leave these zero on Row. This comes before counting the rows' lines,
	// since the bar narrows the body that wraps.
	var bar func(id string) *Badge
	if rs.ShowTearOffBars && rs.TearOffs != nil {
		gold, gray := tearOffBadge(titleFont, v.Brightness, false), tearOffBadge(titleFont, v.Brightness, true)
		bar = func(id string) *Badge { return util.Select(viewEntry{rs.TearOffs, id}.tornOff(ep), gray, gold) }
	}
	rows := styleRows(rs.Rows, v.Brightness.ScaleRGB(colors.view.text), bar)

	// Clamp the persisted scroll offset to the current line count, then
	// skip the rows scrolled off the top. The first row shown may have its
	// first lines scrolled off.
	lines := util.MapSlice(rows, proto.lineCount)
	totalLines := util.ReduceSlice(lines, 0, func(n, sum int) int { return n + sum })
	maxOffset := max(0, totalLines-rs.VisibleRows*rs.MaxCols)
	startIdx, skipLines := 0, 0
	if rs.ScrollState != nil {
		rs.ScrollState.Offset = math.Clamp(rs.ScrollState.Offset, 0, maxOffset)
		skipLines = rs.ScrollState.Offset
		for skipLines > 0 && skipLines >= lines[startIdx] {
			skipLines -= lines[startIdx]
			startIdx++
		}
	}

	// Allocate only as many columns as we'll actually populate.
	remaining := len(rows) - startIdx
	actualCols := 1
	if remaining > 0 {
		actualCols = math.Clamp((remaining+rs.VisibleRows-1)/rs.VisibleRows, 1, rs.MaxCols)
	}
	cols := make([]*RowList, actualCols)
	for c := range cols {
		rl := proto
		cols[c] = &rl
	}
	cols[0].SkipLines = skipLines
	// The row whose pop-up pick area is displayed is drawn selected.
	selectedIdx := -1
	if picked, ok := ep.pickedRow(); ok && picked.View == v.ID {
		selectedIdx = picked.Idx
	}
	for i, r := range rows[startIdx:] {
		col := cols[min(i/rs.VisibleRows, actualCols-1)]
		r.Selected = startIdx+i == selectedIdx
		col.Rows = append(col.Rows, r)
	}

	bodyHeight := util.ReduceMax(slices.Values(cols), 0, (*RowList).Measure)

	// View width: columns side-by-side, optionally widened to fit the title
	// bar with the columns centered (CODE).
	colsTotalW := float32(actualCols) * colWidth
	width := colsTotalW
	colsStart := float32(0)
	if rs.CenterColumnsInTitleBar {
		minW := titleBarMinWidth(titleFont, v.Title, v.TitleButtons)
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
		v.BodyFont = proto.Font
	}

	colExt := func(c int, body math.Extent2D) math.Extent2D {
		x0 := body.P0[0] + colsStart + float32(c)*colWidth
		return math.Extent2D{
			P0: [2]float32{x0, body.P0[1]},
			P1: [2]float32{x0 + colWidth, body.P1[1]},
		}
	}

	v.DrawBody = func(body math.Extent2D, b *ViewBuilders) {
		for c, col := range cols {
			col.Draw(colExt(c, body), b)
		}
	}

	if rs.ScrollState != nil {
		v.Scroll = &ViewScrollConfig{State: rs.ScrollState, MaxOffset: maxOffset}
	}

	// The rows in column c are rs.Rows[startIdx + c*VisibleRows + i].
	v.pickRows = func(body math.Extent2D) []rowPickArea {
		var picks []rowPickArea
		for c, col := range cols {
			colBase := startIdx + c*rs.VisibleRows
			col.iterRows(colExt(c, body), func(i int, _ measuredRow, rowExt math.Extent2D) {
				if r := col.Rows[i]; r.OnClick != nil {
					picks = append(picks, rowPickArea{idx: colBase + i, extent: rowExt, row: r})
				}
			})
		}
		return picks
	}

	if bar != nil {
		v.tearOffBars = func(body math.Extent2D) []tearOffBar {
			var bars []tearOffBar
			for c, col := range cols {
				col.iterRows(colExt(c, body), func(i int, m measuredRow, rowExt math.Extent2D) {
					r := col.Rows[i]
					e := viewEntry{rs.TearOffs, r.TearOff}
					if r.TearOff == "" || m.continued || e.tornOff(ep) {
						return
					}
					// The tear-off's first row lies over this one.
					bars = append(bars, tearOffBar{entry: e, extent: col.badgeExtent(r.Badge, rowExt),
						origin: [2]float32{rowExt.P0[0], rowExt.P1[1] + col.ListTopPad}})
				})
			}
			return bars
		}
	}
}

// rowColumn returns a RowList, without rows, that lays out a column of
// rs's rows.
func (ep *Scope) rowColumn(rs *ViewRowSource, titleFont *renderer.Font) RowList {
	font := ep.ERAMFont(rs.FontIndex)
	lineH := lineHeight(font)
	// A badge column sits flush with the body's left edge so it lines up
	// with the title-bar M button.
	leftChrome := float32(titleBarButtonPad)
	var badgeColumn float32
	if rs.ReserveBadgeColumn {
		badgeColumn = viewMButtonWidth(titleFont)
		leftChrome = badgeColumn
	}
	l := RowList{
		Font:           font,
		LineHeight:     lineH,
		Width:          leftChrome + float32(rs.ContentChars)*charWidth(font) + titleBarButtonPad,
		BadgeColumn:    badgeColumn,
		MaxLines:       rs.VisibleRows,
		HighlightColor: rs.HighlightColor,
	}
	switch rs.RowSpacing {
	case RowSpacingCompact:
		l.ListTopPad = lineH / 4
		l.ListBottomPad = lineH / 4
		l.BottomGap = lineH / 4
	case RowSpacingAiry:
		l.TopPad = lineH / 2
		l.BottomGap = lineH / 2
	}
	return l
}

// styleRows returns a copy of rows, given color where they have none and,
// if bar is non-nil, the tear-off bar it returns for each with a TearOff
// ID.
func styleRows(rows []Row, color renderer.RGB, bar func(id string) *Badge) []Row {
	rows = slices.Clone(rows)
	for i := range rows {
		r := &rows[i]
		if r.Color == (renderer.RGB{}) {
			r.Color = color
		}
		if r.TearOff != "" && bar != nil {
			r.Badge = bar(r.TearOff)
		}
	}
	return rows
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

	// BadgeColumn is the width of the badge column at the left of the
	// list, if it reserves one: the rows' text starts after it whether or
	// not they have badges.
	BadgeColumn float32

	Rows []Row

	// MaxLines caps the total wrapped body lines rendered. The last visible
	// row is truncated mid-content if needed; subsequent rows are dropped.
	// 0 = unlimited.
	MaxLines int

	// SkipLines is the number of the first row's wrapped body lines that
	// are scrolled off the top. The row's badge, label, and AfterDraw
	// belong to its first line and go with it.
	SkipLines int

	// HighlightColor is the fill drawn behind rows with Row.Toggled=true
	// (multi-row toggle highlight, e.g. check list views). Independent of
	// Row.Selected.
	HighlightColor renderer.RGB

	// Populated by Measure.
	measured     []measuredRow
	visibleCount int
}

type measuredRow struct {
	lines     []string // wrapped body; empty if Centered or no Body
	height    float32
	continued bool // the row's first line is scrolled off
}

// labelGapChars is the gap between a row's label and the start of its body
// text, in font characters.
const labelGapChars = 3

// bodyAvailChars returns the body wrap widths in characters for the first
// line (after badge + label + label gap) and continuation lines (after badge
// only).
func (l *RowList) bodyAvailChars(r Row) (firstChars, contChars int) {
	cw := charWidth(l.Font)
	leadingPx := max(float32(titleBarButtonPad), l.BadgeColumn)
	if r.Badge != nil {
		leadingPx = r.Badge.Width + rowBadgeGap
	}
	contChars = int((l.Width - leadingPx - titleBarButtonPad) / cw)
	firstChars = contChars
	if r.Label != "" {
		firstChars -= len(r.Label) + labelGapChars
	}
	return
}

// wrap returns r's body word-wrapped to fit Width, or nil if r is Centered
// or has no Body.
func (l *RowList) wrap(r Row) []string {
	if r.Centered || r.Body == "" {
		return nil
	}
	firstChars, contChars := l.bodyAvailChars(r)
	if lines := wrapWords(r.Body, firstChars, contChars); len(lines) > 0 {
		return lines
	}
	return []string{r.Body}
}

// lineCount returns the number of lines r takes; a spacer takes none.
func (l *RowList) lineCount(r Row) int {
	if r.SpacerHeight > 0 {
		return 0
	}
	return max(len(l.wrap(r)), 1)
}

// Measure pre-wraps each row's body to fit Width and returns the height of
// the visible portion (after SkipLines and MaxLines truncation). Must be
// called before Draw / TextExtents.
func (l *RowList) Measure() float32 {
	l.measured = make([]measuredRow, len(l.Rows))
	for i, r := range l.Rows {
		if r.SpacerHeight > 0 {
			l.measured[i].height = r.SpacerHeight
			continue
		}
		lines := l.wrap(r)
		if i == 0 && l.SkipLines > 0 {
			lines = lines[l.SkipLines:]
			l.measured[i].continued = true
		}
		l.measured[i].lines = lines
		l.measured[i].height = l.TopPad + float32(max(len(lines), 1))*l.LineHeight + l.BottomGap
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

	return util.ReduceSlice(l.measured[:l.visibleCount], l.ListTopPad+l.ListBottomPad,
		func(m measuredRow, h float32) float32 { return h + m.height })
}

// iterRows walks the visible rows top-down, computing each row's full
// extent (full body width × row height) and invoking fn.
func (l *RowList) iterRows(body math.Extent2D, fn func(i int, m measuredRow, ext math.Extent2D)) {
	rowTop := body.P1[1] - l.ListTopPad
	for i := range l.visibleCount {
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
// row: labelX is where the label / continuation body lines start, after
// the row's badge or the list's badge column; bodyX is where the first
// wrapped body line starts (after the label and label-gap, if any).
func (l *RowList) rowContentLayout(r Row, body math.Extent2D) (labelX, bodyX float32) {
	switch {
	case r.Badge != nil:
		labelX = body.P0[0] + r.Badge.Width + 2*titleBarButtonPad
	case l.BadgeColumn > 0:
		labelX = body.P0[0] + l.BadgeColumn
	default:
		labelX = body.P0[0] + titleBarButtonPad
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
	if r.Label != "" && !m.continued {
		fn(r.Label, [2]float32{labelX, baseY})
	}
	for j, line := range m.lines {
		lx := bodyX
		if j > 0 || m.continued {
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
		inkUnion = inkUnion.Union(ink.Offset(pos))
	})
	return inkUnion
}

// Draw renders the visible rows into body. Measure must have been called
// first.
func (l *RowList) Draw(body math.Extent2D, b *ViewBuilders) {
	if l.measured == nil {
		panic("RowList.Draw: Measure must be called first")
	}
	by := cellHeight(l.Font)
	// lineSlack centers each text line in its line cell so the rendered
	// text has equal slack above and below.
	lineSlack := (l.LineHeight - by) / 2

	l.iterRows(body, func(i int, m measuredRow, rowExt math.Extent2D) {
		r := l.Rows[i]
		if r.SpacerHeight > 0 {
			return
		}
		if r.Badge != nil && !m.continued {
			e := l.badgeExtent(r.Badge, rowExt)
			addQuad(b.Trid, e, r.Badge.Border)
			addQuad(b.Trid, e.Expand(-1), r.Badge.Fill)
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
		labelX, bodyX := l.rowContentLayout(r, body)

		// Toggle highlight: a clean bar across the full row extent, drawn
		// behind text. Independent of selection — text color stays as-is.
		if r.Toggled && l.HighlightColor != (renderer.RGB{}) {
			addQuad(b.Trid, rowExt, l.HighlightColor)
		}

		// Selection highlight is drawn behind the text, so compute the
		// tight ink box first and recolor style before the draw pass.
		if r.Selected {
			ink := l.inkUnionOf(r, m, labelX, bodyX, baseY)
			if !ink.IsEmpty() {
				addQuad(b.Trid, ink.Expand(1), colors.popup.backgroundGrey)
				style.Color = colors.popup.backgroundBlack
			}
		}

		l.forBodyTextBits(r, m, labelX, bodyX, baseY, func(s string, pos [2]float32) {
			b.Td.AddText(s, pos, style)
		})
		if r.AfterDraw != nil && !m.continued {
			r.AfterDraw(rowExt, [2]float32{bodyX, baseY}, b)
		}
	})
}

// badgeExtent returns where a badge goes on the row with extent rowExt:
// at its left, centered vertically on its first line's "0" glyph ink. At
// larger fonts the layout cell has noticeably more space below the glyph
// than the badge does, so it is aligned to the ink rather than the cell.
func (l *RowList) badgeExtent(badge *Badge, rowExt math.Extent2D) math.Extent2D {
	lineSlack := (l.LineHeight - cellHeight(l.Font)) / 2
	ink := l.Font.InkBounds("0", 0)
	y := rowExt.P1[1] - l.TopPad - lineSlack + (ink.P0[1]+ink.P1[1])/2
	x := rowExt.P0[0] + titleBarButtonPad
	return math.Extent2D{
		P0: [2]float32{x, y - badge.Height/2},
		P1: [2]float32{x + badge.Width, y + badge.Height/2},
	}
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
// pickAreaPopup

// pickAreaPopup is a pop-up pick area: a single pick area displayed beside
// what was clicked, such as the DELETE pop-up beside a view's entry, which
// performs its function when it is picked. Any click closes it. picked is
// the view's row it was opened from, if it was, which is drawn selected
// while it is displayed.
type pickAreaPopup struct {
	origin [2]float32
	width  float32
	height float32
	text   string
	picked *RowClick
	font   *renderer.Font
	onPick func()
}

// rowPicker is a pop-up that may have been opened from a view's row, which
// it reports.
type rowPicker interface {
	pickedRow() (RowClick, bool)
}

func (d *pickAreaPopup) pickedRow() (RowClick, bool) {
	if d.picked == nil {
		return RowClick{}, false
	}
	return *d.picked, true
}

// pickedRow returns the view's row that the displayed pop-up was opened
// from, if it was opened from one.
func (ep *Scope) pickedRow() (RowClick, bool) {
	if rp, ok := ep.popup.(rowPicker); ok {
		return rp.pickedRow()
	}
	return RowClick{}, false
}

// openRowPickArea displays a pop-up pick area beside a view's row, which
// is drawn selected while the pop-up is displayed.
func (ep *Scope) openRowPickArea(ctx *scope.Context, c RowClick, text string, onPick func()) {
	p := ep.openPickAreaPopup(ctx, c.Extent, text, onPick)
	p.picked = &c
}

// openPickAreaPopup displays a pop-up pick area beside the extent of what
// was clicked, flipping left if it would otherwise overflow the pane, and
// warps the cursor to its center so the user can pick it without moving the
// mouse.
func (ep *Scope) openPickAreaPopup(ctx *scope.Context, beside math.Extent2D, text string, onPick func()) *pickAreaPopup {
	font := ep.ERAMFont(2)
	pad := float32(8)
	width := float32(len(text))*charWidth(font) + 2*pad
	height := cellHeight(font) + pad

	pe := ctx.DrawExtent
	gap := float32(4)
	rowCenterY := (beside.P0[1] + beside.P1[1]) / 2
	origin := [2]float32{beside.P1[0] + gap, rowCenterY + height/2}
	if origin[0]+width > pe.P1[0] {
		origin[0] = beside.P0[0] - gap - width
	}
	origin[0] = max(origin[0], pe.P0[0])
	if origin[1]-height < pe.P0[1] {
		origin[1] = pe.P0[1] + height
	}
	origin[1] = min(origin[1], pe.P1[1])

	ctx.SetMousePosition([2]float32{origin[0] + width/2, origin[1] - height/2})

	p := &pickAreaPopup{
		origin: origin,
		width:  width,
		height: height,
		text:   text,
		font:   font,
		onPick: onPick,
	}
	ep.popup = p
	return p
}

func (d *pickAreaPopup) draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
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
	ep.popupExtent = extent
	hovered := mouse != nil && extent.Inside(mouse.Pos)

	addQuad(trid, extent, ps.Brightness.Button.ScaleRGB(colors.popup.backgroundGrey))

	outlineColor := colors.menu.rowDimOutline
	if hovered {
		outlineColor = colors.menu.rowHoverOutline
	}
	ld.AddLineLoop(ps.Brightness.Border.ScaleRGB(outlineColor), extent.Corners())

	td.AddTextCentered(d.text, extent.Center(),
		renderer.TextStyle{Font: d.font, Color: ps.Brightness.Text.ScaleRGB(colors.popup.text)})

	transforms.LoadWindowViewingMatrices(cb)
	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)

	if ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse) {
		if hovered {
			d.onPick()
		}
		ep.popup = nil
		ep.clearMousePrimaryConsumed(mouse)
		ep.clearMouseTertiaryConsumed(mouse)
	}
}

// eram/view.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"slices"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
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

	// Width is the body *content* width. DrawView adds an additional 2
	// space characters (per BodyFont) of reserve on the right when Scroll
	// is non-nil so the scroll bar can be drawn without overlapping content.
	Width      float32
	BodyHeight float32

	Title     string
	TitleFont *renderer.Font

	// BodyFont is the font used for measuring the body-related layout that
	// DrawView itself performs — currently just the 2-space scroll-bar
	// reserve. Falls back to TitleFont when nil.
	BodyFont *renderer.Font

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
	// MinimizeTarget is shorthand for the OnMinimize closure: when set and
	// OnMinimize is nil, DrawView synthesizes `func() { *MinimizeTarget = false }`.
	// Every multi-row view's minimize handler is exactly that.
	MinimizeTarget *bool

	OnBodyTertiaryMenu func(host math.Extent2D) popup

	Body func(extent math.Extent2D, b *ViewBuilders)

	Scroll *ViewScrollConfig

	Selectable *ViewSelectable

	// RowSource, when set, supplies a column-major list-of-rows body.
	// DrawView builds the per-column RowLists, distributes Rows across them,
	// computes Width / BodyHeight, sets up Scroll.MaxOffset, and
	// auto-generates Selectable.Items. Callers must NOT also set Width,
	// BodyHeight, Body, Scroll, or Selectable when RowSource is set —
	// applyRowSource overwrites them.
	RowSource *ViewRowSource
}

// ViewRowSource declaratively describes a column-major list-of-rows body for
// a View. See View.RowSource. Callers describe what to show (rows, font
// size, content width) — the View handles column layout, scroll, padding,
// badge geometry, side pads, and the default text color.
type ViewRowSource struct {
	// Rows is the flat slice of rows to display, in display order. DrawView
	// distributes them column-major (the first VisibleRows fill column 0,
	// the next VisibleRows fill column 1, etc). Row.Color is filled with
	// the View's default text color (Brightness.ScaleRGB(colors.view.text))
	// where the row leaves it zero.
	Rows []Row

	// FontSize selects ERAMFont(FontSize); clamped to [1,3].
	FontSize int

	// ContentWidth is the per-column body content width, in pixels,
	// excluding the badge column and side pads (View adds those).
	ContentWidth float32

	MaxCols     int
	VisibleRows int

	// BadgeColumn reserves the leftmost badge column matching the title-bar
	// M button. Implied by BadgesVisible.
	BadgeColumn bool

	// BadgesVisible draws the standard yellow fill in the badge column,
	// scaled by the View's Brightness. Implies BadgeColumn.
	BadgesVisible bool

	// RowSpacing selects the per-row vertical padding style.
	RowSpacing RowSpacing

	// ScrollState, when non-nil, enables the scroll bar. MaxOffset is
	// computed as max(0, len(Rows) - VisibleRows*MaxCols) — i.e. the last
	// row sits at the bottom of the visible area.
	ScrollState *ViewScrollState

	// CenterColumnsInTitleBar widens the view to at least the title bar's
	// minimum width and horizontally centers the column block when the title
	// bar is the binding constraint. Used by CODE.
	CenterColumnsInTitleBar bool

	// EmptyKeepsColumnWidth keeps width = column width (rather than
	// collapsing to 0) when Rows is empty. Used by ALTIM SET so the title
	// bar still renders at a reasonable size.
	EmptyKeepsColumnWidth bool

	// SelectedID is the row whose ID matches gets the selection highlight
	// (matches Row.ID).
	SelectedID string

	// Selectable wires the click-to-delete affordance using row IDs. If
	// SelectableState is nil, no selection is set.
	SelectableState    *ViewSelectionState
	SelectableOnDelete func(label string)

	// OnRowExtents, if non-nil, is called after each column's Draw with the
	// visible-row extents flattened in display order (col 0 rows, then col 1
	// rows, ...) and the row index of the first visible row. Used by CRR
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

// deleteByID removes the first element of *sl whose key (extracted by
// idFn) equals id. ViewSelectable.OnDelete callbacks for list views all
// follow this pattern.
func deleteByID[T any](sl *[]T, id string, idFn func(T) string) {
	for i, v := range *sl {
		if idFn(v) == id {
			*sl = slices.Delete(*sl, i, i+1)
			return
		}
	}
}

// drawRectOutline draws a 1px line-loop around ex.
func drawRectOutline(ld *renderer.ColoredLinesDrawBuilder, ex math.Extent2D, color renderer.RGB) {
	q0 := [2]float32{ex.P0[0] - 1, ex.P0[1] - 1}
	q1 := [2]float32{ex.P1[0] + 1, ex.P0[1] - 1}
	q2 := [2]float32{ex.P1[0] + 1, ex.P1[1] + 1}
	q3 := [2]float32{ex.P0[0] - 1, ex.P1[1] + 1}
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

// viewMPad is the horizontal padding on each side of the title-bar M and "-"
// buttons. Exported via viewMButtonWidth so callers can align body decorations
// (e.g. row badges) with the title-bar M.
const viewMPad = float32(4)

// viewMButtonWidth returns the on-screen width of the title-bar M button.
// Callers that want a body element to line up directly below the M (badges,
// indicators) should size it with this.
func viewMButtonWidth(titleFont *renderer.Font) float32 {
	return titleFont.LayoutBounds("M", 0).Width() + 2*viewMPad
}

// viewTitleHeight returns the on-screen height of the title bar. Matches the
// computation in DrawView so callers (e.g. badges that want to match the M
// button's visual size) can use it without poking at DrawView internals.
func viewTitleHeight(titleFont *renderer.Font) float32 {
	return max(16, titleFont.LayoutBounds("M", 0).Height()+4)
}

// titleBarMinWidth is the minimum width the title bar needs to fit the M
// button on the left, the centered Title text, and the "-" button on the
// right without overlap. Title text is centered at width/2, so the bound is
// twice the wider of the two side buttons plus the title text width.
func titleBarMinWidth(titleFont *renderer.Font, title string) float32 {
	mButtonW := viewMButtonWidth(titleFont)
	minButtonW := titleFont.LayoutBounds("-", 0).Width() + 2*viewMPad
	titleTextW := titleFont.LayoutBounds(title, 0).Width()
	return titleTextW + 2*max(mButtonW, minButtonW)
}

// lineHeight is the row line height ERAM views use: the font cell height
// plus a 2 px inter-row gap. Identical formula everywhere — keep it here so
// callers stop re-deriving it.
func lineHeight(font *renderer.Font) float32 {
	return font.LayoutBounds("0", 0).Height() + 2
}

// defaultBadge builds the standard row badge: width and height matched to
// the title-bar M button, viewMPad horizontal pad, configurable fill, and
// the default badge border color.
func defaultBadge(titleFont *renderer.Font, fill renderer.RGB) *Badge {
	return &Badge{
		Width:  titleFont.LayoutBounds("M", 0).Width(),
		Height: viewTitleHeight(titleFont),
		Pad:    viewMPad,
		Fill:   fill,
		Border: colors.badge.border,
	}
}

// titleBarState bundles the title-bar's three clickable regions with their
// per-frame hover state. Computed once in DrawView and reused by both the
// rendering pass and the click dispatcher so the two never disagree.
type titleBarState struct {
	mRect, minRect, titleRect       math.Extent2D
	insideM, insideMin, insideTitle bool
}

// titleBarLayout computes the three clickable title-bar regions and their
// hover flags from the bar's outer geometry. titleP0 is the bar's top-left
// (= the view's top-left), width is the full view width, titleH the bar
// height.
func titleBarLayout(titleP0 [2]float32, width, titleH float32, v View, mouse *platform.MouseState) titleBarState {
	titleP1 := math.Add2f(titleP0, [2]float32{width, 0})
	titleP2 := math.Add2f(titleP1, [2]float32{0, -titleH})
	var s titleBarState
	if v.OnMenu != nil {
		s.mRect = math.Extent2D{
			P0: [2]float32{titleP0[0], titleP0[1] - titleH},
			P1: [2]float32{titleP0[0] + viewMButtonWidth(v.TitleFont), titleP0[1]},
		}
		s.insideM = mouse != nil && s.mRect.Inside(mouse.Pos)
	}
	if v.OnMinimize != nil {
		minw := v.TitleFont.LayoutBounds("-", 0).Width()
		s.minRect = math.Extent2D{
			P0: [2]float32{titleP1[0] - 2*viewMPad - minw, titleP1[1] - titleH},
			P1: titleP1,
		}
		s.insideMin = mouse != nil && s.minRect.Inside(mouse.Pos)
	}
	leftEdge := titleP0[0]
	rightEdge := titleP1[0]
	if v.OnMenu != nil {
		leftEdge = s.mRect.P1[0]
	}
	if v.OnMinimize != nil {
		rightEdge = s.minRect.P0[0]
	}
	s.titleRect = math.Extent2D{
		P0: [2]float32{leftEdge, titleP2[1]},
		P1: [2]float32{rightEdge, titleP1[1]},
	}
	s.insideTitle = mouse != nil && s.titleRect.Inside(mouse.Pos)
	return s
}

// drawTitleButton writes a centered title-bar button label (e.g. "M", "-")
// to td. The outline is drawn separately so the dim outlines for all three
// regions can be emitted before the bright ones.
func drawTitleButton(td *renderer.TextDrawBuilder, label string, rect math.Extent2D, font *renderer.Font, dim, bright renderer.RGB, hovered bool) {
	color := dim
	if hovered {
		color = bright
	}
	td.AddTextCentered(label, rect.Center(), renderer.TextStyle{Font: font, Color: color})
}

// scrollBarLayout returns the two clickable / drawable rectangles for the
// scroll-bar arrows, given the body geometry. Used once by click handling
// and once by rendering so the two stay in lock-step.
func scrollBarLayout(bodyP0, bodyP1 [2]float32, bodyH float32) (upRect, downRect math.Extent2D) {
	sectionH := (bodyH - 2 - scrollBarGap) / 2
	scrollX1 := bodyP1[0] - 1
	scrollX0 := scrollX1 - scrollBarTotalW
	upY1 := bodyP0[1] - 1
	upY0 := upY1 - sectionH
	downY0 := upY0 - scrollBarGap
	downY1 := downY0 - sectionH
	upRect = math.Extent2D{P0: [2]float32{scrollX0, upY0}, P1: [2]float32{scrollX1, upY1}}
	// downY1 < downY0 because y increases upward; canonical Extent2D wants P0 < P1.
	downRect = math.Extent2D{P0: [2]float32{scrollX0, downY1}, P1: [2]float32{scrollX1, downY0}}
	return
}

// drawScrollArrow renders one of the scroll-bar chevrons: a stair-stepped
// triangle whose tip is at (centerX, tipY) and whose 10-row body extends in
// the +y direction when dir = +1 or −y direction when dir = −1.
func drawScrollArrow(ld *renderer.ColoredLinesDrawBuilder, centerX, tipY float32, dir int, color renderer.RGB) {
	for i, w := range [...]float32{1, 1, 3, 3, 5, 5, 7, 7, 9, 9} {
		y := tipY + float32(dir*i)
		ld.AddLine([2]float32{centerX - w/2, y}, [2]float32{centerX + w/2, y}, color)
	}
}

// scrollReserveWidth is the right-edge area DrawView reserves for the scroll
// bar when Scroll is configured. Sized to 2 BodyFont space characters so it
// scales with the body font; at least scrollBarTotalW so the scroll-bar
// visual still fits when the body font is tiny.
func scrollReserveWidth(v View) float32 {
	if v.Scroll == nil {
		return 0
	}
	font := v.BodyFont
	if font == nil {
		font = v.TitleFont
	}
	spaceW := font.LayoutBounds(" ", 0).Width()
	return max(2*spaceW, scrollBarTotalW)
}

// viewTextColor returns the standard list-view text color (colors.view.text)
// scaled by the given Brightness level (0..100). Callers whose AfterDraw
// closures need the row's text color use this so they don't have to know
// about colors.view.text.
func (ep *ERAMPane) viewTextColor(brightnessLevel int) renderer.RGB {
	return radar.Brightness(brightnessLevel).ScaleRGB(colors.view.text)
}

// applyRowSource fills in Width, BodyHeight, BodyFont, Body, Scroll, and
// Selectable on v from v.RowSource. Called once at the top of DrawView when
// RowSource is set so the rest of DrawView can treat the view as if the
// caller had wired those fields up directly.
//
// The View owns the "boring" geometry: font selection, line height, badge
// column reservation, side padding, default text color. The caller supplies
// only the row data, the per-column content width, and a handful of high-level
// flags (BadgeColumn, BadgesVisible, RowSpacing, scroll/select wiring).
func (ep *ERAMPane) applyRowSource(v *View) {
	rs := v.RowSource

	font := ep.clampedFont(rs.FontSize, 1, 3)
	lineH := lineHeight(font)
	textColor := v.Brightness.ScaleRGB(colors.view.text)

	// BadgesVisible implies the badge column is reserved.
	badgeColumn := rs.BadgeColumn || rs.BadgesVisible

	// Per-column total width: side pad + (badge column or side pad) +
	// content + side pad. viewMButtonWidth matches the badge draw size
	// (badge.Width + 2*badge.Pad), so the badge sits flush with body.P0.
	sidePad := viewMPad
	leftChrome := sidePad
	if badgeColumn {
		leftChrome = viewMButtonWidth(v.TitleFont)
	}
	colWidth := leftChrome + rs.ContentWidth + sidePad

	// Standard yellow badge, built once and shared by every row.
	var badge *Badge
	if rs.BadgesVisible {
		badge = defaultBadge(v.TitleFont, v.Brightness.ScaleRGB(colors.badge.fill))
	}

	// Prototype RowList. SidePad is always viewMPad; per-spacing padding
	// follows RowSpacing.
	proto := RowList{
		Font:              font,
		LineHeight:        lineH,
		SidePad:           sidePad,
		SelectedID:        rs.SelectedID,
		SelectedBgColor:   colors.popup.backgroundGrey,
		SelectedTextColor: colors.popup.backgroundBlack,
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

	// Clamp the persisted scroll offset to the current row count and
	// compute the start row.
	maxPerPage := rs.VisibleRows * rs.MaxCols
	maxOffset := max(0, len(rs.Rows)-maxPerPage)
	startIdx := 0
	if rs.ScrollState != nil {
		rs.ScrollState.Offset = math.Clamp(rs.ScrollState.Offset, 0, maxOffset)
		startIdx = rs.ScrollState.Offset
	}

	// Only allocate as many columns as we'll actually populate.
	remaining := len(rs.Rows) - startIdx
	actualCols := 1
	if remaining > 0 {
		actualCols = math.Clamp((remaining+rs.VisibleRows-1)/rs.VisibleRows, 1, rs.MaxCols)
	}

	cols := make([]*RowList, actualCols)
	for c := range cols {
		rl := proto
		rl.Width = colWidth
		rl.MaxLines = rs.VisibleRows
		rl.Rows = nil
		cols[c] = &rl
	}
	for i := range remaining {
		c := i / rs.VisibleRows
		if c >= actualCols {
			break
		}
		r := rs.Rows[startIdx+i]
		// Default the row's text color and badge so simple callers leave
		// these zero on Row.
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

	// View width: columns side-by-side, optionally widened so the title bar
	// fits and the columns are then centered (CODE).
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

	colBodyExtent := func(c int, body math.Extent2D) math.Extent2D {
		return math.Extent2D{
			P0: [2]float32{body.P0[0] + colsStart + float32(c)*colWidth, body.P0[1]},
			P1: [2]float32{body.P0[0] + colsStart + float32(c+1)*colWidth, body.P1[1]},
		}
	}

	v.Body = func(body math.Extent2D, b *ViewBuilders) {
		var allExtents []math.Extent2D
		for c, col := range cols {
			ext := col.Draw(colBodyExtent(c, body), b)
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
			Font:     v.TitleFont,
			OnDelete: rs.SelectableOnDelete,
			Items: func(body math.Extent2D) []ViewSelectableItem {
				return SelectableItems(cols, body, colBodyExtent)
			},
		}
	}
}

// DrawView renders a View, processing chrome and clicks in this order:
// scroll bar → title-bar buttons → title-bar drag → body-tertiary menu →
// body-primary drag → finalize in-progress drag. Each handler consumes its
// click so Body sees only fall-through events.
func (ep *ERAMPane) DrawView(ctx *panes.Context, transforms radar.ScopeTransformations,
	cb *renderer.CommandBuffer, v View) {

	mouse := ctx.Mouse

	// Default TitleFont so callers don't have to thread it through. Every
	// ERAM view uses ERAMFont(2) for the title bar.
	if v.TitleFont == nil {
		v.TitleFont = ep.ERAMFont(2)
	}

	// Default OnMinimize to "set MinimizeTarget = false" when callers
	// supply only the target — that's the only thing every minimize
	// handler does.
	if v.OnMinimize == nil && v.MinimizeTarget != nil {
		target := v.MinimizeTarget
		v.OnMinimize = func() { *target = false }
	}

	// RowSource fills in Width/BodyHeight/Body/Scroll/Selectable.
	if v.RowSource != nil {
		ep.applyRowSource(&v)
	}

	// v.Width is the body content width; DrawView grows the view to make
	// room for the scroll bar on the right when Scroll is configured.
	scrollReserveW := scrollReserveWidth(v)
	contentWidth := v.Width
	width := contentWidth + scrollReserveW

	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	hasTitle := v.Title != ""

	// Title-bar height and the dim title-bar text color come from TitleFont
	// and Brightness; consumers below build their own TextStyle from these.
	var titleH float32
	var titleDimColor renderer.RGB
	if hasTitle {
		titleDimColor = v.Brightness.ScaleRGB(colors.view.text)
		titleH = viewTitleHeight(v.TitleFont)
	}

	bodyH := v.BodyHeight
	totalH := titleH + bodyH

	// When this view's configuration pop-up is open, pin the view's edge
	// adjacent to the pop-up. The pop-up's origin is fixed at open time, so
	// width changes (font, columns, …) move the view's *other* edge instead
	// of pushing into the pop-up.
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
	// bodyExtent is the area passed to Body and used for body-area click
	// handling — it excludes the right-edge scroll-bar reserve so callers
	// don't accidentally render content under the scroll bar.
	bodyExtent := math.Extent2D{
		P0: bodyP3,
		P1: [2]float32{bodyP1[0] - scrollReserveW, bodyP1[1]},
	}

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
	var tb titleBarState
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

		tb = titleBarLayout(titleP0, width, titleH, v, mouse)
		bright := colors.view.hoveredOutline

		// Title text is centered on the full title-bar width (so it stays
		// visually centered in the bar regardless of button presence),
		// while the M / "-" buttons center within their own rects.
		titleColor := titleDimColor
		if tb.insideTitle {
			titleColor = bright
		}
		td.AddTextCentered(v.Title,
			[2]float32{titleP0[0] + width/2, titleP0[1] - titleH/2},
			renderer.TextStyle{Font: v.TitleFont, Color: titleColor})

		if v.OnMenu != nil {
			drawTitleButton(td, "M", tb.mRect, v.TitleFont, titleDimColor, bright, tb.insideM)
		}
		if v.OnMinimize != nil {
			drawTitleButton(td, "-", tb.minRect, v.TitleFont, titleDimColor, bright, tb.insideMin)
		}

		// Dim outlines first, then bright outlines on hover.
		dim := colors.view.buttonOutline
		if v.OnMenu != nil && !tb.insideM {
			drawRectOutline(ld, tb.mRect, dim)
		}
		if v.OnMinimize != nil && !tb.insideMin {
			drawRectOutline(ld, tb.minRect, dim)
		}
		if !tb.insideTitle {
			drawRectOutline(ld, tb.titleRect, dim)
		}
		if v.OnMenu != nil && tb.insideM {
			drawRectOutline(ld, tb.mRect, bright)
		}
		if v.OnMinimize != nil && tb.insideMin {
			drawRectOutline(ld, tb.minRect, bright)
		}
		if tb.insideTitle {
			drawRectOutline(ld, tb.titleRect, bright)
		}
	}

	// Scroll-bar geometry (computed up front so click handling and rendering
	// agree).
	scrollVisible := v.Scroll != nil && v.Scroll.MaxOffset > 0 && bodyH > 0
	var scrollUpRect, scrollDownRect math.Extent2D
	if scrollVisible {
		scrollUpRect, scrollDownRect = scrollBarLayout(bodyP0, bodyP1, bodyH)
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
		// host is the view's outer extent — clamped to the window-frame
		// corners — passed to menu-opening callbacks so they can place
		// the popup relative to the view.
		host := math.Extent2D{P0: [2]float32{p0[0], p2[1]}, P1: [2]float32{p0[0] + width, p0[1]}}
		startDrag := func() {
			ep.viewRepo.activeID = v.ID
			ep.viewRepo.startTime = time.Now()
			ep.viewRepo.dragOffset = math.Sub2f(mouse.Pos, p0)
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

		case hasTitle && v.OnMenu != nil && tb.insideM:
			ep.popup = v.OnMenu(host)
			ep.consumeMouseClick(mouse)

		case hasTitle && v.OnMinimize != nil && tb.insideMin:
			v.OnMinimize()
			ep.consumeMouseClick(mouse)

		case hasTitle && tb.insideTitle && ep.viewRepo.activeID == "":
			startDrag()
			ep.consumeMouseClick(mouse)

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
			ep.popup = v.OnBodyTertiaryMenu(host)
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
		scrollBg := colors.scroll.background
		scrollBorder := colors.scroll.border
		arrowColor := colors.scroll.arrow

		fillBordered := func(r math.Extent2D) {
			q0 := [2]float32{r.P0[0], r.P1[1]}
			q1 := [2]float32{r.P1[0], r.P1[1]}
			q2 := [2]float32{r.P1[0], r.P0[1]}
			q3 := [2]float32{r.P0[0], r.P0[1]}
			trid.AddQuad(q0, q1, q2, q3, scrollBg)
			ld.AddLine(q0, q1, scrollBorder)
			ld.AddLine(q1, q2, scrollBorder)
			ld.AddLine(q2, q3, scrollBorder)
			ld.AddLine(q3, q0, scrollBorder)
		}
		fillBordered(scrollUpRect)
		fillBordered(scrollDownRect)

		centerX := (scrollUpRect.P0[0] + scrollUpRect.P1[0]) / 2
		// Up arrow: tip 1 px below the top of the up section, body extends
		// downward (−y) inside the section.
		drawScrollArrow(ld, centerX, scrollUpRect.P1[1]-1, -1, arrowColor)
		// Down arrow: tip 1 px above the bottom of the down section, body
		// extends upward (+y) inside the section.
		drawScrollArrow(ld, centerX, scrollDownRect.P0[1]+1, +1, arrowColor)
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
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

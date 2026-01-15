package eram

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
)

///////////////////////////////////////////////////////////////////////////////
// CRR Types
///////////////////////////////////////////////////////////////////////////////

// CRRColor represents one of the selectable CRR colors. The palette
// approximates the figures provided by the user.
type CRRColor int

const (
	CRRGreen CRRColor = iota
	CRRYellow
	CRRMagenta
	CRRCyan
	CRRWhite
	CRRAmber
)

// BaseRGB returns the unscaled RGB for the CRR color.
func (c CRRColor) BaseRGB() renderer.RGB {
	switch c {
	case CRRGreen:
		return renderer.RGB{R: .1, G: .9, B: .1}
	case CRRYellow:
		return renderer.RGBFromHex(0xB7B513)
	case CRRMagenta:
		return renderer.RGBFromHex(0xB000B0)
	case CRRCyan:
		return renderer.RGB{R: 0, G: .8, B: .8}
	case CRRWhite:
		return renderer.RGB{R: .85, G: .85, B: .85}
	case CRRAmber:
		return renderer.RGB{R: .9, G: .7, B: .2}
	default:
		return renderer.RGB{R: .85, G: .85, B: .85}
	}
}

// BrightRGB applies an ERAM brightness to the base color.
func (c CRRColor) BrightRGB(b radar.Brightness) renderer.RGB {
	return b.ScaleRGB(c.BaseRGB())
}

// CRRGroup holds one CRR group definition and its aircraft membership.
type CRRGroup struct {
	Label    string
	Location math.Point2LL
	Color    CRRColor
	// Aircraft present in the group; value unused.
	Aircraft map[av.ADSBCallsign]struct{}
}

var crrLabelRE = regexp.MustCompile(`^[A-Z0-9]{1,5}$`)

func validCRRLabel(s string) bool {
	return crrLabelRE.MatchString(s)
}

///////////////////////////////////////////////////////////////////////////////
// CRR Command Helpers
///////////////////////////////////////////////////////////////////////////////

var reFRD = regexp.MustCompile(`^([A-Z0-9]{3,5})(\d{3})(\d{3})$`)

// tryExtractLocation scans the input text for an embedded location character
// added via Input.AddLocation and returns it if present.
func tryExtractLocation(it inputText) (math.Point2LL, bool) {
	for _, ic := range it {
		if string(ic.char) == locationSymbol {
			return math.Point2LL{ic.location[0], ic.location[1]}, true
		}
	}
	return math.Point2LL{}, false
}

// parseCRRLocation parses location tokens used by LF: //FIX, //FRD,
// //lat/long. Returns true if a location was resolved.
func parseCRRLocation(ctx *panes.Context, token string) (math.Point2LL, bool) {
	s := strings.TrimPrefix(strings.ToUpper(token), "//")
	// FRD?
	if m := reFRD.FindStringSubmatch(s); len(m) == 4 {
		base, ok := ctx.Client.State.Locate(m[1])
		if !ok {
			return math.Point2LL{}, false
		}
		hdg, _ := strconv.Atoi(m[2])
		distInt, _ := strconv.Atoi(m[3])
		dist := float32(distInt)
		return math.Offset2LL(base, float32(hdg), dist, ctx.NmPerLongitude, ctx.MagneticVariation), true
	}
	// Lat/long, fix, navaid, airport
	if p, ok := ctx.Client.State.Locate(s); ok {
		return p, true
	}
	return math.Point2LL{}, false
}

// resolveAircraftTokens converts a slash-separated list of ACIDs/FLIDs/CIDs to callsigns.
func resolveAircraftTokens(ctx *panes.Context, s string) []av.ADSBCallsign {
	var out []av.ADSBCallsign
	for tok := range strings.SplitSeq(s, "/") {
		tok = strings.ToUpper(strings.TrimSpace(tok))
		if tok == "" {
			continue
		}
		if trk, ok := ctx.Client.State.GetTrackByFLID(tok); ok {
			out = append(out, trk.ADSBCallsign)
			continue
		}
		if trk, ok := ctx.Client.State.GetTrackByACID(sim.ACID(tok)); ok {
			out = append(out, trk.ADSBCallsign)
			continue
		}
		// CID match
		for _, t := range ctx.Client.State.Tracks {
			if t.FlightPlan.CID == tok {
				out = append(out, t.ADSBCallsign)
				break
			}
		}
	}
	return out
}

// drawCRRView renders the Continuous Range Readout view.
func (ep *ERAMPane) drawCRRView(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if !ps.CRR.Visible {
		return
	}

	// Ensure session state.
	if ep.crrGroups == nil {
		ep.crrGroups = make(map[string]*CRRGroup)
	}

	// Sizing and styling
	font := ep.ERAMFont(2) // TODO: Check for windows font size
	textStyle := renderer.TextStyle{
		Font:        font,
		Color:       radar.Brightness(ps.CRR.Bright).ScaleRGB(renderer.RGB{R: .85, G: .85, B: .85}),
		LineSpacing: 0,
	}

	// Compute baseline line height using font metrics.
	_, by := font.BoundText("0", textStyle.LineSpacing)
	lineH := float32(by + 2)

	// Basic pane geometry
	p0 := ps.CRR.Position
	width := float32(260)
	titleH := float32(16)
	if _, tby := font.BoundText("CRR", textStyle.LineSpacing); tby > 0 {
		titleH = float32(tby) + 4
	}

	// Build quick lookup of current aircraft positions (needed for content calculation)
	trackPos := make(map[av.ADSBCallsign]math.Point2LL)
	for _, trk := range ep.visibleTracks(ctx) {
		trackPos[trk.ADSBCallsign] = trk.Location
	}

	// Sort group labels
	labels := make([]string, 0, len(ep.crrGroups))
	for label := range ep.crrGroups {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	// Calculate actual content height (limited by LINES setting)
	maxLines := int(math.Clamp(float32(ps.CRR.Lines), 1, 100))
	contentLines := 0
	numSpacers := 0
	for _, label := range labels {
		if contentLines >= maxLines {
			break
		}
		g := ep.crrGroups[label]
		if g == nil {
			continue
		}
		contentLines++ // group label
		if contentLines >= maxLines {
			break
		}
		// Count aircraft entries
		acCount := 0
		for cs := range g.Aircraft {
			if _, ok := trackPos[cs]; ok {
				acCount++
			}
		}
		// Add aircraft up to maxLines
		acToAdd := acCount
		if contentLines+acToAdd > maxLines {
			acToAdd = maxLines - contentLines
		}
		contentLines += acToAdd
		numSpacers++ // spacer after each group
	}

	// Height based on actual content. If no groups, show only the title bar.
	var contentHeight float32
	if len(labels) == 0 {
		contentHeight = 0
	} else if contentLines == 0 {
		contentHeight = lineH + 8 // minimum 1 line for empty list view
	} else {
		contentHeight = lineH*float32(contentLines) + float32(numSpacers)*2 + 8
	}
	height := titleH + contentHeight

	bodyHeight := height - titleH

	p1 := math.Add2f(p0, [2]float32{width, 0})
	p2 := math.Add2f(p1, [2]float32{0, -height})
	p3 := math.Add2f(p2, [2]float32{-width, 0})

	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	// Draw body only when there is content (avoids stray black box in empty panel mode).
	if bodyHeight > 0 {
		bodyP0 := math.Add2f(p0, [2]float32{0, -titleH})
		bodyP1 := math.Add2f(bodyP0, [2]float32{width, 0})
		bodyP2 := math.Add2f(bodyP1, [2]float32{0, -bodyHeight})
		bodyP3 := math.Add2f(bodyP0, [2]float32{-width, -bodyHeight})
		trid.AddQuad(bodyP0, bodyP1, bodyP2, bodyP3, renderer.RGB{R: 0, G: 0, B: 0})
	}

	// Border
	if ps.CRR.ShowBorder {
		borderColor := ps.Brightness.Border.ScaleRGB(renderer.RGB{R: .914, G: .914, B: .914})
		ld.AddLine(p0, p1, borderColor)
		ld.AddLine(p1, p2, borderColor)
		ld.AddLine(p2, p3, borderColor)
		ld.AddLine(p3, p0, borderColor)
	}

	titleP0 := p0
	titleP1 := math.Add2f(p0, [2]float32{width, 0})
	titleP2 := math.Add2f(p1, [2]float32{0, -titleH})
	titleP3 := math.Add2f(p0, [2]float32{0, -titleH})
	titleBarColor := renderer.RGB{R: 0, G: 0, B: 0} // black for T mode
	if ps.CRR.Opaque {
		titleBarColor = renderer.RGB{R: 0.6, G: 0.6, B: 0.6} // gray for O mode
	}
	trid.AddQuad(titleP0, titleP1, titleP2, titleP3, titleBarColor)

	mouse := ctx.Mouse

	// Title text centered
	title := "CRR"
	tw, th := font.BoundText(title, textStyle.LineSpacing)
	titlePos := math.Add2f(titleP0, [2]float32{width/2 - float32(tw)/2, -titleH/2 + float32(th)/2})
	titleColor := ps.Brightness.Text.ScaleRGB(renderer.RGB{R: .85, G: .85, B: .85})

	// Left M button, right minimize
	mLabel := "M"
	mw, mh := font.BoundText(mLabel, textStyle.LineSpacing)
	mPad := float32(2)
	// Extent2D.Inside expects P0 = min corner, P1 = max corner
	mRect := math.Extent2D{
		P0: [2]float32{titleP0[0], titleP0[1] - titleH},                    // bottom-left (min)
		P1: [2]float32{titleP0[0] + mPad + float32(mw) + mPad, titleP0[1]}, // top-right (max)
	}

	minLabel := "-"
	minw, minh := font.BoundText(minLabel, textStyle.LineSpacing)
	// Extent2D.Inside expects P0 = min corner, P1 = max corner
	minRect := math.Extent2D{
		P0: [2]float32{titleP1[0] - mPad - float32(minw) - mPad, titleP1[1] - titleH}, // bottom-left (min)
		P1: titleP1,                                                                   // top-right (max)
	}
	// Hover detection for buttons and title
	mouseInsideM := mouse != nil && mRect.Inside(mouse.Pos)
	mouseInsideMin := mouse != nil && minRect.Inside(mouse.Pos)
	titleRect := math.Extent2D{
		P0: [2]float32{mRect.P1[0], titleP2[1]}, // bottom-left
		P1: [2]float32{minRect.P0[0], titleP1[1]},
	}
	mouseInsideTitle := mouse != nil && titleRect.Inside(mouse.Pos)

	if mouseInsideTitle {
		titleColor = toolbarHoveredOutlineColor
	}
	td.AddText(title, titlePos, renderer.TextStyle{Font: font, Color: titleColor})

	mTextColor := textStyle.Color
	if mouseInsideM {
		mTextColor = toolbarHoveredOutlineColor
	}
	td.AddText(mLabel, math.Add2f(titleP0, [2]float32{mPad, -titleH/2 + float32(mh)/2}), renderer.TextStyle{
		Font:        font,
		Color:       mTextColor,
		LineSpacing: textStyle.LineSpacing,
	})

	minTextColor := textStyle.Color
	if mouseInsideMin {
		minTextColor = toolbarHoveredOutlineColor
	}
	td.AddText(minLabel, [2]float32{minRect.P0[0] + mPad, titleP1[1] - titleH/2 + float32(minh)/2}, renderer.TextStyle{
		Font:        font,
		Color:       minTextColor,
		LineSpacing: textStyle.LineSpacing,
	})

	// Button/title outlines with hover highlight
	// Draw non-hovered first, then hovered so bright lines overwrite dim ones
	drawRectOutline := func(ex math.Extent2D, color renderer.RGB) {
		rp0 := [2]float32{ex.P0[0], ex.P0[1]}
		rp1 := [2]float32{ex.P1[0], ex.P0[1]}
		rp2 := [2]float32{ex.P1[0], ex.P1[1]}
		rp3 := [2]float32{ex.P0[0], ex.P1[1]}
		ld.AddLine(rp0, rp1, color)
		ld.AddLine(rp1, rp2, color)
		ld.AddLine(rp2, rp3, color)
		ld.AddLine(rp3, rp0, color)
	}
	// Draw non-hovered outlines first
	if !mouseInsideM {
		drawRectOutline(mRect, toolbarOutlineColor)
	}
	if !mouseInsideMin {
		drawRectOutline(minRect, toolbarOutlineColor)
	}
	if !mouseInsideTitle {
		drawRectOutline(titleRect, toolbarOutlineColor)
	}
	// Draw hovered outlines last so they overwrite shared edges
	if mouseInsideM {
		drawRectOutline(mRect, toolbarHoveredOutlineColor)
	}
	if mouseInsideMin {
		drawRectOutline(minRect, toolbarHoveredOutlineColor)
	}
	if mouseInsideTitle {
		drawRectOutline(titleRect, toolbarHoveredOutlineColor)
	}

	// Clicks for title buttons (mouse.Pos is already in pane-local window coordinates)
	if mouse := ctx.Mouse; mouse != nil && (mouse.Clicked[platform.MouseButtonPrimary] || mouse.Clicked[platform.MouseButtonTertiary]) {
		switch {
		case mRect.Inside(mouse.Pos):
			ctx.SetMousePosition(math.Add2f(mouse.Pos, [2]float32{width * 1.5, 0}))
			ep.crrMenuOpen = !ep.crrMenuOpen
		case minRect.Inside(mouse.Pos):
			ps.CRR.Visible = false
		case titleRect.Inside(mouse.Pos) && !ep.crrReposition:
			ep.crrReposition = true
			ep.crrRepoStart = time.Now()
			ep.crrDragOffset = math.Sub2f(mouse.Pos, p0)
			ctx.Platform.StartCaptureMouse(ctx.PaneExtent)
		case ep.crrReposition && time.Since(ep.crrRepoStart) > 100*time.Millisecond:
			ps.CRR.Position = math.Sub2f(mouse.Pos, ep.crrDragOffset)
			ep.crrReposition = false
			ctx.Platform.EndCaptureMouse()
		}
	}

	// Reposition preview outline while dragging
	if ep.crrReposition && mouse != nil {
		previewP0 := math.Sub2f(mouse.Pos, ep.crrDragOffset)
		previewP1 := math.Add2f(previewP0, [2]float32{width, 0})
		previewP2 := math.Add2f(previewP1, [2]float32{0, -height})
		previewP3 := math.Add2f(previewP0, [2]float32{0, -height})
		c := toolbarHoveredOutlineColor
		ld.AddLine(previewP0, previewP1, c)
		ld.AddLine(previewP1, previewP2, c)
		ld.AddLine(previewP2, previewP3, c)
		ld.AddLine(previewP3, previewP0, c)
	}

	// Content origin is below the title bar with small padding.
	cursor := math.Add2f(titleP3, [2]float32{4, -2})

	// Menu, if open (drawn to the right side of the title by default)
	if ep.crrMenuOpen {
		menuOrigin := math.Add2f(titleP3, [2]float32{width, titleH})
		ep.drawCRRMenu(ctx, menuOrigin, 150, cb)
	}

	// Draw groups
	ep.crrLabelRects = make(map[string]math.Extent2D)
	ep.crrAircraftRects = make(map[string]map[av.ADSBCallsign]math.Extent2D)

	linesRemaining := maxLines

	// Panel mode: just draw group buttons row
	if !ps.CRR.ListMode {
		x := cursor[0]
		y := cursor[1]
		for _, label := range labels {
			g := ep.crrGroups[label]
			if g == nil {
				continue
			}
			txt := strings.ToUpper(g.Label)
			tw, th := font.BoundText(txt, textStyle.LineSpacing)
			w := float32(tw) + 16
			h := float32(th) + 8
			bp0 := [2]float32{x, y}
			bp1 := math.Add2f(bp0, [2]float32{w, 0})
			bp2 := math.Add2f(bp1, [2]float32{0, -h})
			bp3 := math.Add2f(bp0, [2]float32{0, -h})
			trid.AddQuad(bp0, bp1, bp2, bp3, renderer.RGB{R: 0, G: 0, B: 0})
			ld.AddLine(bp0, bp1, ps.Brightness.Border.ScaleRGB(renderer.RGB{R: .6, G: .6, B: .6}))
			ld.AddLine(bp1, bp2, ps.Brightness.Border.ScaleRGB(renderer.RGB{R: .6, G: .6, B: .6}))
			ld.AddLine(bp2, bp3, ps.Brightness.Border.ScaleRGB(renderer.RGB{R: .6, G: .6, B: .6}))
			ld.AddLine(bp3, bp0, ps.Brightness.Border.ScaleRGB(renderer.RGB{R: .6, G: .6, B: .6}))
			td.AddText(txt, math.Add2f(bp0, [2]float32{8, -h/2 + float32(th)/2}), renderer.TextStyle{
				Font:        font,
				Color:       g.Color.BrightRGB(radar.Brightness(math.Clamp(float32(ps.CRR.ColorBright[g.Color]), 0, 100))),
				LineSpacing: 0,
			})
			// P0 = min corner, P1 = max corner for Inside to work
			ep.crrLabelRects[label] = math.Extent2D{P0: bp3, P1: bp1}
			x += w + 8
		}
		// Emit drawing and return
		transforms.LoadWindowViewingMatrices(cb)
		trid.GenerateCommands(cb)
		ld.GenerateCommands(cb)
		td.GenerateCommands(cb)
		// Handle clicks on panel labels
		if mouse := ctx.Mouse; mouse != nil && (mouse.Clicked[platform.MouseButtonPrimary] || mouse.Clicked[platform.MouseButtonTertiary]) {
			for label, rect := range ep.crrLabelRects {
				if rect.Inside(mouse.Pos) {
					if mouse.Clicked[platform.MouseButtonPrimary] {
						ep.Input.Set(ps, "LF "+strings.ToUpper(label)+" ")
					} else {
						if g := ep.crrGroups[label]; g != nil {
							if len(g.Aircraft) > 0 {
								g.Aircraft = make(map[av.ADSBCallsign]struct{})
							} else {
								delete(ep.crrGroups, label)
							}
						}
					}
					break
				}
			}
		}
		return
	}

	for _, label := range labels {
		if linesRemaining <= 0 {
			break
		}
		g := ep.crrGroups[label]
		if g == nil {
			continue
		}
		// Group header (centered)
		groupStyle := textStyle
		groupStyle.Color = g.Color.BrightRGB(radar.Brightness(math.Clamp(float32(ps.CRR.ColorBright[g.Color]), 0, 100)))
		gw, _ := font.BoundText(g.Label, groupStyle.LineSpacing)
		centerX := p0[0] + width/2 - float32(gw)/2
		labelPos := [2]float32{centerX, cursor[1]}
		td.AddText(strings.ToUpper(g.Label), labelPos, groupStyle)
		// clickable rect around centered label - P0 = min corner, P1 = max corner
		ep.crrLabelRects[label] = math.Extent2D{
			P0: [2]float32{labelPos[0], labelPos[1] - lineH},       // bottom-left (min)
			P1: [2]float32{labelPos[0] + float32(gw), labelPos[1]}, // top-right (max)
		}
		cursor = math.Add2f(cursor, [2]float32{0, -lineH})
		linesRemaining--

		if linesRemaining <= 0 {
			break
		}

		// Build distance list
		type distEntry struct {
			Callsign av.ADSBCallsign
			DistNM   float32
		}
		var entries []distEntry
		for cs := range g.Aircraft {
			if pos, ok := trackPos[cs]; ok {
				entries = append(entries, distEntry{Callsign: cs, DistNM: math.NMDistance2LL(pos, g.Location)})
			}
		}
		slices.SortFunc(entries, func(a, b distEntry) int {
			if a.DistNM < b.DistNM {
				return -1
			} else if a.DistNM > b.DistNM {
				return 1
			}
			return 0
		})
		// Draw aircraft with right-aligned distances
		// init per-group aircraft rects
		ep.crrAircraftRects[label] = make(map[av.ADSBCallsign]math.Extent2D)
		for _, e := range entries {
			if linesRemaining <= 0 {
				break
			}
			cs := fmt.Sprintf("%s    %.1f", e.Callsign, e.DistNM)
			td.AddText(cs, cursor, textStyle)
			// clickable row extent spans full width for easier click - P0 = min corner, P1 = max corner
			rowRect := math.Extent2D{
				P0: [2]float32{cursor[0], cursor[1] - lineH},     // bottom-left (min)
				P1: [2]float32{cursor[0] + width - 8, cursor[1]}, // top-right (max)
			}
			ep.crrAircraftRects[label][e.Callsign] = rowRect
			cursor = math.Add2f(cursor, [2]float32{0, -lineH})
			linesRemaining--
		}
		// Spacer
		cursor = math.Add2f(cursor, [2]float32{0, -2})
	}

	// Handle clicks on labels and aircraft rows after layout
	if mouse := ctx.Mouse; mouse != nil && (mouse.Clicked[platform.MouseButtonPrimary] || mouse.Clicked[platform.MouseButtonTertiary]) {
		// Group labels
		for label, rect := range ep.crrLabelRects {
			if rect.Inside(mouse.Pos) {
				if mouse.Clicked[platform.MouseButtonPrimary] {
					// Seed LF command
					ep.Input.Set(ps, "LF "+strings.ToUpper(label)+" ")
				} else {
					// Middle click: delete contents or group
					if g := ep.crrGroups[label]; g != nil {
						if len(g.Aircraft) > 0 {
							g.Aircraft = make(map[av.ADSBCallsign]struct{})
						} else {
							delete(ep.crrGroups, label)
						}
					}
				}
				break
			}
		}
		// Aircraft rows
		for label, rows := range ep.crrAircraftRects {
			for cs, rect := range rows {
				if rect.Inside(mouse.Pos) {
					// Toggle remove on either primary or middle click
					if g := ep.crrGroups[label]; g != nil && g.Aircraft != nil {
						if _, ok := g.Aircraft[cs]; ok {
							delete(g.Aircraft, cs)
						}
					}
					break
				}
			}
		}
	}

	// Emit drawing
	transforms.LoadWindowViewingMatrices(cb)
	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

// drawCRRMenu draws the CRR configuration menu when ep.crrMenuOpen is true.
func (ep *ERAMPane) drawCRRMenu(ctx *panes.Context, origin [2]float32, width float32, cb *renderer.CommandBuffer) {
	if !ep.crrMenuOpen {
		return
	}
	ps := ep.currentPrefs()

	font := ep.ERAMToolbarFont()
	itemH := float32(18)
	textColor := renderer.RGB{R: 1, G: 1, B: 1}
	baseBg := eramGray
	greenBg := CRRGreen.BrightRGB(radar.Brightness(100))
	blackBg := renderer.RGB{R: 0, G: 0, B: 0}

	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	cursor := origin
	// Create title row for menu

	{
		rp0 := cursor
		rp1 := math.Add2f(cursor, [2]float32{width, 0})
		rp2 := math.Add2f(rp1, [2]float32{0, -itemH})
		rp3 := math.Add2f(rp0, [2]float32{0, -itemH})
		trid.AddQuad(rp0, rp1, rp2, rp3, baseBg)
		ld.AddLine(rp0, rp1, eramGray.Scale(.25))
		style := renderer.TextStyle{
			Font:        font,
			Color:       textColor,
			LineSpacing: 0,
		}
		title := "CRR"
		centerX := rp0[0] + width/2
		centerY := rp0[1] - itemH/2
		td.AddTextCentered(title, [2]float32{centerX, centerY}, style)
		// Now, bring the cursor to the side to draw the X box
		label := "X"
		textStyle := renderer.TextStyle{
			Font:        font,
			Color:       textColor,
			LineSpacing: 0,
		}
		dw, _ := font.BoundText(label, textStyle.LineSpacing)
		right := math.Add2f(cursor, [2]float32{width, -itemH})
		distPos := math.Add2f(cursor, [2]float32{right[0] - (cursor[0] + float32(dw)), -1})
		td.AddText(label, distPos, textStyle)
		ld.AddLine(cursor, [2]float32{cursor[0], right[1]}, blackBg)
		cursor = rp3
	}

	// Helper to draw a full-width row

	isFirstRow := true
	row := func(label string, bg renderer.RGB, color renderer.RGB, centered bool) math.Extent2D {
		rp0 := cursor
		rp1 := math.Add2f(cursor, [2]float32{width, 0})
		rp2 := math.Add2f(rp1, [2]float32{0, -itemH})
		rp3 := math.Add2f(rp0, [2]float32{0, -itemH})
		trid.AddQuad(rp0, rp1, rp2, rp3, bg)
		style := renderer.TextStyle{
			Font:        font,
			Color:       color,
			LineSpacing: 0,
		}
		if centered {
			centerX := rp0[0] + width/2
			centerY := rp0[1] - itemH/2
			td.AddTextCentered(label, [2]float32{centerX, centerY}, style)
		} else {
			td.AddText(label, math.Add2f(rp0, [2]float32{4, -itemH + 12}), style)
		}

		cursor = rp3
		extent := math.Extent2D{P0: rp3, P1: rp1}

		dimColor := eramGray.Scale(.25)
		brightColor := eramGray.Scale(.8)
		hovered := ctx.Mouse != nil && extent.Inside(ctx.Mouse.Pos)

		if hovered {
			// draw box
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

		isFirstRow = false
		return extent
	}

	rtLabel := "T"
	rtBcg := blackBg
	if ps.CRR.Opaque {
		rtLabel = "O"
		rtBcg = baseBg
	}
	rt := row(rtLabel, rtBcg, textColor, true)
	rb := row("BORDER", baseBg, textColor, false)
	rLines := row("LINES "+strconv.Itoa(ps.CRR.Lines), greenBg, textColor, false)
	rFont := row("FONT "+strconv.Itoa(ps.CRR.Font), greenBg, textColor, false)
	rBright := row("BRIGHT "+strconv.Itoa(ps.CRR.Bright), greenBg, textColor, false)
	rList := row("LIST", baseBg, textColor, false)

	rColor := row("COLOR "+strconv.Itoa(ps.CRR.ColorBright[ps.CRR.SelectedColor]), blackBg, CRRGreen.BrightRGB(radar.Brightness(90)), false)
	type swatch struct {
		c   CRRColor
		x0  float32
		x1  float32
		box math.Extent2D
	}
	var swatches []swatch
	swCols := 2
	swH := itemH
	swColors := []CRRColor{CRRGreen, CRRMagenta, CRRWhite, CRRAmber}
	swRows := (len(swColors) + swCols - 1) / swCols

	// Button size and gaps between buttons
	swatchW := float32(60)
	swatchH := float32(12)
	swatchGap := float32(8) // horizontal gap

	totalSwatchWidth := swatchW*float32(swCols) + swatchGap*float32(swCols-1)
	startX := origin[0] + (width-totalSwatchWidth)/2

	// Background behind swatches
	bgP0 := cursor
	bgP1 := math.Add2f(bgP0, [2]float32{width, 0})
	bgP2 := math.Add2f(bgP1, [2]float32{0, -float32(swRows) * swH})
	bgP3 := math.Add2f(bgP0, [2]float32{0, -float32(swRows) * swH})
	trid.AddQuad(bgP0, bgP1, bgP2, bgP3, blackBg)

	for i, c := range swColors {
		rowIdx := i / swCols
		colIdx := i % swCols
		// Position from the centered starting point
		x0 := startX + float32(colIdx)*(swatchW+swatchGap)
		yTop := cursor[1] - float32(rowIdx)*swH
		yOffset := (swH - swatchH) / 2
		sp0 := [2]float32{x0, yTop - yOffset}
		sp1 := [2]float32{x0 + swatchW, yTop - yOffset}
		sp2 := [2]float32{x0 + swatchW, yTop - yOffset - swatchH}
		sp3 := [2]float32{x0, yTop - yOffset - swatchH}
		trid.AddQuad(sp0, sp1, sp2, sp3, c.BrightRGB(radar.Brightness(ps.CRR.ColorBright[c])))
		if ps.CRR.SelectedColor == c {
			ld.AddLineLoop(renderer.RGB{R: 1, G: 1, B: 1}, [][2]float32{sp0, sp1, sp2, sp3})
		}
		// P0 = min corner, P1 = max corner for Inside to work
		swatches = append(swatches, swatch{
			c: c,
			box: math.Extent2D{
				P0: [2]float32{sp3[0], sp2[1]},
				P1: [2]float32{sp1[0], sp0[1]},
			},
		})
	}
	cursor = math.Add2f(cursor, [2]float32{0, -float32(swRows) * swH})

	// CRR group labels section
	groupLabels := make([]string, 0, len(ep.crrGroups))
	for l := range ep.crrGroups {
		groupLabels = append(groupLabels, l)
	}
	sort.Strings(groupLabels)
	groupRows := make(map[string]math.Extent2D)
	for _, l := range groupLabels {
		groupRows[l] = row(strings.ToUpper(l), blackBg, ep.crrGroups[l].Color.BrightRGB(radar.Brightness(math.Clamp(float32(ps.CRR.ColorBright[ep.crrGroups[l].Color]), 0, 100))), false)
	}
	// Draw border around the entire menu
	p0 := origin
	p1 := [2]float32{origin[0] + width, origin[1]}
	p2 := [2]float32{origin[0] + width, cursor[1]}
	p3 := [2]float32{origin[0], cursor[1]}
	ld.AddLineLoop(renderer.RGB{R: 1, G: 1, B: 1}, [][2]float32{p0, p1, p2, p3})

	// Create a pane extent for the entire menu. If clicked outside, close the menu.
	menuExtent := math.Extent2D{
		P0: p3,
		P1: p1,
	}

	if mouse := ctx.Mouse; mouse != nil && (mouse.Clicked[platform.MouseButtonPrimary] || mouse.Clicked[platform.MouseButtonTertiary]) {
		if !menuExtent.Inside(mouse.Pos) {
			ep.crrMenuOpen = false
		}
	}

	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)

	if mouse := ctx.Mouse; mouse != nil && (mouse.Clicked[platform.MouseButtonPrimary] || mouse.Clicked[platform.MouseButtonTertiary]) {
		// O/T
		if rt.Inside(mouse.Pos) {
			ps.CRR.Opaque = !ps.CRR.Opaque
			return
		}
		if rb.Inside(mouse.Pos) {
			ps.CRR.ShowBorder = !ps.CRR.ShowBorder
			return
		}
		if rLines.Inside(mouse.Pos) {
			if mouse.Clicked[platform.MouseButtonPrimary] {
				ps.CRR.Lines = int(math.Clamp(float32(ps.CRR.Lines-1), 1, 100))
			} else {
				ps.CRR.Lines = int(math.Clamp(float32(ps.CRR.Lines+1), 1, 100))
			}
			return
		}
		if rFont.Inside(mouse.Pos) {
			if mouse.Clicked[platform.MouseButtonPrimary] {
				ps.CRR.Font--
				if ps.CRR.Font < 1 {
					ps.CRR.Font = 4
				}
			} else {
				ps.CRR.Font++
				if ps.CRR.Font > 4 {
					ps.CRR.Font = 1
				}
			}
			return
		}
		if rBright.Inside(mouse.Pos) {
			if mouse.Clicked[platform.MouseButtonPrimary] {
				ps.CRR.Bright = int(math.Clamp(float32(ps.CRR.Bright-1), 0, 100))
			} else {
				ps.CRR.Bright = int(math.Clamp(float32(ps.CRR.Bright+1), 0, 100))
			}
			return
		}
		if rList.Inside(mouse.Pos) {
			ps.CRR.ListMode = !ps.CRR.ListMode
			return
		}
		if rColor.Inside(mouse.Pos) {
			// Adjust brightness of selected color
			if mouse.Clicked[platform.MouseButtonPrimary] {
				ps.CRR.ColorBright[ps.CRR.SelectedColor] = int(math.Clamp(float32(ps.CRR.ColorBright[ps.CRR.SelectedColor]-1), 0, 100))
			} else {
				ps.CRR.ColorBright[ps.CRR.SelectedColor] = int(math.Clamp(float32(ps.CRR.ColorBright[ps.CRR.SelectedColor]+1), 0, 100))
			}
			return
		}
		// Swatch select
		for _, s := range swatches {
			if s.box.Inside(mouse.Pos) {
				ps.CRR.SelectedColor = s.c
				return
			}
		}
		// Assign color to group
		for label, ex := range groupRows {
			if ex.Inside(mouse.Pos) {
				if g := ep.crrGroups[label]; g != nil {
					g.Color = ps.CRR.SelectedColor
				}
				return
			}
		}
	}
}

// drawCRRFixes draws clickable CRR fix labels when enabled under ATC TOOLS.
func (ep *ERAMPane) drawCRRFixes(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if !ps.CRR.DisplayFixes {
		return
	}

	font := ep.ERAMFont(ps.CRR.Font)

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	ep.crrFixRects = make(map[string]math.Extent2D)

	// Show existing CRR groups as neon-green asterisk plus label at group location.
	fixLabels := make([]string, 0, len(ep.crrGroups))
	for l := range ep.crrGroups {
		fixLabels = append(fixLabels, l)
	}
	sort.Strings(fixLabels)
	for _, l := range fixLabels {
		// Get the color for the CRR fix 
		fixColor := ep.crrGroups[l].Color.BrightRGB(radar.Brightness(math.Clamp(float32(ps.CRR.ColorBright[ps.CRR.SelectedColor]), 0, 100)))
		style := renderer.TextStyle{Font: font, Color: fixColor}

		g := ep.crrGroups[l]
		if g == nil {
			continue
		}
		p := transforms.WindowFromLatLongP(g.Location)
		// Draw asterisk then label with a space
		td.AddText("*", p, style)
		ax, _ := font.BoundText("*", style.LineSpacing)
		lp := math.Add2f(p, [2]float32{float32(ax) + 4, 0})
		label := strings.ToUpper(g.Label)
		td.AddText(label, lp, style)
		w, h := font.BoundText("* "+label, style.LineSpacing)
		// P0 = min corner, P1 = max corner for Inside to work
		ep.crrFixRects[l] = math.Extent2D{
			P0: [2]float32{p[0], p[1] - float32(h)}, // bottom-left (min)
			P1: [2]float32{p[0] + float32(w), p[1]}, // top-right (max)
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)

	// Handle click to seed LF if input is empty
	if mouse := ctx.Mouse; mouse != nil && (mouse.Clicked[platform.MouseButtonPrimary] || mouse.Clicked[platform.MouseButtonTertiary]) && len(ep.Input) == 0 {
		for id, ex := range ep.crrFixRects {
			if ex.Inside(mouse.Pos) {
				ep.Input.Set(ps, "LF "+strings.ToUpper(id)+" ")
				break
			}
		}
	}
}

///////////////////////////////////////////////////////////////////////////////
// CRR Distance Display (next to aircraft tags)
///////////////////////////////////////////////////////////////////////////////

// drawCRRDistances draws CRR distance values next to aircraft tags for aircraft
// that are members of CRR groups. The distance is displayed in the color of the
// CRR group the aircraft belongs to.
func (ep *ERAMPane) drawCRRDistances(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if ep.crrGroups == nil || len(ep.crrGroups) == 0 {
		return
	}

	// Build a map of callsign -> (group, distance)
	type crrEntry struct {
		group  *CRRGroup
		distNM float32
	}
	acCRR := make(map[av.ADSBCallsign]crrEntry)

	for _, trk := range ep.visibleTracks(ctx) {
		for _, g := range ep.crrGroups {
			if _, ok := g.Aircraft[trk.ADSBCallsign]; ok {
				trkState := ep.TrackState[trk.ADSBCallsign]
				dist := math.NMDistance2LL(trkState.track.Location, g.Location)
				acCRR[trk.ADSBCallsign] = crrEntry{group: g, distNM: dist}
				break // aircraft can only be in one group
			}
		}
	}

	if len(acCRR) == 0 {
		return
	}

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	font := ep.ERAMFont(ps.FDBSize)

	for _, trk := range ep.visibleTracks(ctx) {
		entry, ok := acCRR[trk.ADSBCallsign]
		if !ok {
			continue
		}

		state := ep.TrackState[trk.ADSBCallsign]
		if state == nil {
			continue
		}

		// Get position below the track target
		location  := state.track.Location
		trackWin := transforms.WindowFromLatLongP(location)

		// Position the distance text below and to the left of the track
		distStr := fmt.Sprintf("%.1f", entry.distNM)
		dw, dh := font.BoundText(distStr, 0)

		// Position offset: below and left of the track symbol
		pos := [2]float32{
			trackWin[0] - float32(dw) - 8,
			trackWin[1] - float32(dh) - 4,
		}

		// Use the CRR group color
		color := entry.group.Color.BrightRGB(radar.Brightness(math.Clamp(float32(ps.CRR.ColorBright[entry.group.Color]), 0, 100)))

		td.AddText(distStr, pos, renderer.TextStyle{
			Font:        font,
			Color:       color,
			LineSpacing: 0,
		})
	}

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

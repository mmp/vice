package eram

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
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
		return colors.crr.green
	case CRRYellow:
		return colors.crr.yellow
	case CRRMagenta:
		return colors.crr.magenta
	case CRRCyan:
		return colors.crr.cyan
	case CRRWhite:
		return colors.crr.white
	case CRRAmber:
		return colors.crr.amber
	default:
		return colors.crr.white
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
			return ic.location, true
		}
	}
	return math.Point2LL{}, false
}

// parseLocation parses location tokens used by LF: //FIX, //FRD,
// //lat/long. Returns true if a location was resolved.
func parseLocation(ctx *panes.Context, token string) (math.Point2LL, bool) {
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
		return math.Offset2LL(base, math.MagneticToTrue(math.MagneticHeading(hdg), ctx.MagneticVariation),
			dist, ctx.NmPerLongitude), true
	}
	// Lat/long, fix, navaid, airport
	if p, ok := ctx.Client.State.Locate(s); ok {
		return p, true
	}
	return math.Point2LL{}, false
}

// drawCRRView renders the Continuous Range Readout view.
func (ep *ERAMPane) drawCRRView(ctx *panes.Context, tracks []sim.Track, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if !ps.CRR.Visible {
		return
	}
	if ep.CRRGroups == nil {
		ep.CRRGroups = make(map[string]*CRRGroup)
	}

	font := ep.ERAMFont(2)
	const width = float32(260)

	trackPos := make(map[av.ADSBCallsign]math.Point2LL)
	for _, trk := range tracks {
		trackPos[trk.ADSBCallsign] = trk.Location
	}
	labels := util.SortedMapKeys(ep.CRRGroups)

	v := View{
		Position:   &ps.CRR.Position,
		ID:         "crr",
		Width:      width,
		Title:      "CRR",
		Opaque:     ps.CRR.Opaque,
		ShowBorder: ps.CRR.ShowBorder,
		Brightness: ps.CRR.Bright,
		// 8 static rows + 2 swatch rows + one row per group (the title row
		// is added by makeViewMenu).
		OnMenu: ep.makeViewMenu(ctx, "crr", 8+2+len(ep.CRRGroups),
			func(pb popupBase) popup { return &crrPopup{popupBase: pb} }),
		MinimizeTarget: &ps.CRR.Visible,
	}
	switch {
	case len(labels) == 0:
		ep.crrLabelRects = nil
		ep.crrAircraftRects = nil
	case !ps.CRR.ListMode:
		v.BodyHeight, v.Body = ep.buildCRRPanel(labels, font)
	default:
		v.RowSource = ep.buildCRRList(labels, trackPos)
	}
	ep.DrawView(ctx, transforms, cb, v)

	// Click handling: DrawView consumed title-bar/drag clicks. Anything left
	// is for the body's group/aircraft rects.
	mouse := ctx.Mouse
	if !(ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse)) {
		return
	}
	primary := ep.mousePrimaryClicked(mouse)
	for label, rect := range ep.crrLabelRects {
		if !rect.Inside(mouse.Pos) {
			continue
		}
		if primary {
			ep.Input.Set(ps, "LF "+strings.ToUpper(label)+" ")
		} else if g := ep.CRRGroups[label]; g != nil {
			if len(g.Aircraft) > 0 {
				g.Aircraft = make(map[av.ADSBCallsign]struct{})
			} else {
				delete(ep.CRRGroups, label)
			}
		}
		return
	}
	for label, rows := range ep.crrAircraftRects {
		for cs, rect := range rows {
			if !rect.Inside(mouse.Pos) {
				continue
			}
			if g := ep.CRRGroups[label]; g != nil && g.Aircraft != nil {
				delete(g.Aircraft, cs)
			}
			return
		}
	}
}

// buildCRRPanel constructs the panel-mode body: horizontal row of buttons,
// one per group. Body height = button height + small top margin.
func (ep *ERAMPane) buildCRRPanel(labels []string, font *renderer.Font) (float32, func(math.Extent2D, *ViewBuilders)) {
	ps := ep.currentPrefs()
	bodyHeight := font.LayoutBounds("X", 0).Height() + 8 + 4

	return bodyHeight, func(body math.Extent2D, b *ViewBuilders) {
		ep.crrLabelRects = make(map[string]math.Extent2D)
		ep.crrAircraftRects = make(map[string]map[av.ADSBCallsign]math.Extent2D)
		borderColor := ps.Brightness.Border.ScaleRGB(colors.crr.panelBorder)
		x := body.P0[0] + 4
		y := body.P1[1] - 2
		for _, label := range labels {
			g := ep.CRRGroups[label]
			if g == nil {
				continue
			}
			txt := strings.ToUpper(g.Label)
			text := font.LayoutBounds(txt, 0)
			tw, th := text.Width(), text.Height()
			w := tw + 16
			h := th + 8
			bp0 := [2]float32{x, y}
			bp1 := math.Add2f(bp0, [2]float32{w, 0})
			bp2 := math.Add2f(bp1, [2]float32{0, -h})
			bp3 := math.Add2f(bp0, [2]float32{0, -h})
			b.Trid.AddQuad(bp0, bp1, bp2, bp3, colors.crr.panelButtonBackground)
			b.Ld.AddLine(bp0, bp1, borderColor)
			b.Ld.AddLine(bp1, bp2, borderColor)
			b.Ld.AddLine(bp2, bp3, borderColor)
			b.Ld.AddLine(bp3, bp0, borderColor)
			b.Td.AddText(txt, math.Add2f(bp0, [2]float32{8, -h/2 + th/2}), renderer.TextStyle{
				Font:  font,
				Color: g.Color.BrightRGB(ps.CRR.ColorBright[g.Color]),
			})
			ep.crrLabelRects[label] = math.Extent2D{P0: bp3, P1: bp1}
			x += w + 8
		}
	}
}

// buildCRRList constructs the list-mode RowSource: per-group header +
// sorted aircraft rows separated by 2px spacers. A parallel metadata slice
// lets the OnRowExtents callback populate crrLabelRects / crrAircraftRects
// after the rows are drawn. Group-header rows set Color explicitly; aircraft
// rows leave Color zero and let the View fill it.
func (ep *ERAMPane) buildCRRList(labels []string, trackPos map[av.ADSBCallsign]math.Point2LL) *ViewRowSource {
	ps := ep.currentPrefs()

	type rowMeta struct {
		label    string
		callsign av.ADSBCallsign // empty = group header
		spacer   bool
	}

	var rows []Row
	var metas []rowMeta

	for _, label := range labels {
		g := ep.CRRGroups[label]
		if g == nil {
			continue
		}
		// Group header (centered, colored)
		rows = append(rows, Row{
			Label:    strings.ToUpper(g.Label),
			Color:    g.Color.BrightRGB(ps.CRR.ColorBright[g.Color]),
			Centered: true,
		})
		metas = append(metas, rowMeta{label: label})

		// Distance-sorted aircraft
		type dist struct {
			cs av.ADSBCallsign
			d  float32
		}
		var entries []dist
		for cs := range g.Aircraft {
			if pos, ok := trackPos[cs]; ok {
				entries = append(entries, dist{cs, math.NMDistance2LL(pos, g.Location)})
			}
		}
		slices.SortFunc(entries, func(a, b dist) int {
			switch {
			case a.d < b.d:
				return -1
			case a.d > b.d:
				return 1
			}
			return 0
		})
		for _, e := range entries {
			rows = append(rows, Row{Label: fmt.Sprintf("%s    %.1f", e.cs, e.d)})
			metas = append(metas, rowMeta{label: label, callsign: e.cs})
		}

		rows = append(rows, Row{SpacerHeight: 2})
		metas = append(metas, rowMeta{spacer: true})
	}

	// 18 ContentChars * font-2 space-width (14 px) = 252 px content,
	// + 2*viewMPad side pad = the same 260 px total column width as panel mode.
	return &ViewRowSource{
		Rows:         rows,
		FontSize:     2,
		ContentChars: 18,
		MaxCols:      1,
		VisibleRows:  ps.CRR.Lines,
		RowSpacing:   RowSpacingCompact,
		OnRowExtents: func(first int, extents []math.Extent2D) {
			ep.crrLabelRects = make(map[string]math.Extent2D)
			ep.crrAircraftRects = make(map[string]map[av.ADSBCallsign]math.Extent2D)
			for i, ext := range extents {
				m := metas[first+i]
				switch {
				case m.spacer:
					// no click
				case m.callsign == "":
					ep.crrLabelRects[m.label] = ext
				default:
					if ep.crrAircraftRects[m.label] == nil {
						ep.crrAircraftRects[m.label] = make(map[av.ADSBCallsign]math.Extent2D)
					}
					ep.crrAircraftRects[m.label][m.callsign] = ext
				}
			}
		},
	}
}

// crrPopup is the popup-interface impl for the CRR configuration menu. The
// origin is captured at open time from the view's current geometry.
type crrPopup struct {
	popupBase
}

func (c *crrPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	origin := c.origin
	const width = viewPopupWidth

	rows := []ERAMMenuItem{
		ep.makeBooleanMenuItem(&ps.CRR.Opaque, "O", "T"),
		{Label: "BORDER", BgColor: colors.popup.backgroundGrey, Color: colors.popup.text, OnClick: func(_ ERAMMenuClickType) bool {
			ps.CRR.ShowBorder = !ps.CRR.ShowBorder
			return false
		}},
		makeIntMenuItem(ep, &ps.CRR.Lines, "LINES", 1, 100, 1),
		{Label: "FONT " + strconv.Itoa(ps.CRR.Font), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, OnClick: func(ct ERAMMenuClickType) bool {
			if ct == MenuClickPrimary {
				ps.CRR.Font = 1 + (ps.CRR.Font+2)%4
			} else {
				ps.CRR.Font = 1 + ps.CRR.Font%4
			}
			return false
		}},
		makeIntMenuItem(ep, &ps.CRR.Bright, "BRIGHT", 0, 100, 1),
		ep.makeToggleMenuItem(&ps.CRR.ListMode, "LIST"),
		{Label: fmt.Sprintf("COLOR %d", ps.CRR.ColorBright[ps.CRR.SelectedColor]), BgColor: colors.popup.backgroundBlack,
			Color: CRRGreen.BrightRGB(radar.Brightness(90)), OnClick: func(_ ERAMMenuClickType) bool {
				v := ps.CRR.ColorBright[ps.CRR.SelectedColor]
				handleClick(ep, &v, 0, 100, 1)
				ps.CRR.ColorBright[ps.CRR.SelectedColor] = v
				return false
			}},
	}

	// Sort group labels for the custom content closure below.
	groupLabels := util.SortedMapKeys(ep.CRRGroups)

	cfg := ERAMMenuConfig{
		Title: "CRR",
		Width: width,
		Font:  ep.ERAMFont(2),
		Rows:  rows,

		// Color swatches + group label rows drawn between the static rows and
		// the menu border, preserving the original visual order.
		CustomContent: func(cursor [2]float32, w float32,
			trid *renderer.ColoredTrianglesDrawBuilder,
			ld *renderer.ColoredLinesDrawBuilder,
			td *renderer.TextDrawBuilder,
			mouse *platform.MouseState) [2]float32 {

			bButton := ps.Brightness.Button
			bBorder := ps.Brightness.Border
			bText := ps.Brightness.Text

			itemH := float32(18)
			font := ep.ERAMFont(2)

			// Color swatches
			swCols := 2
			swColors := []CRRColor{CRRGreen, CRRMagenta, CRRWhite, CRRAmber}
			swRows := (len(swColors) + swCols - 1) / swCols
			swH := itemH
			swatchW := float32(60)
			swatchH := float32(12)
			swatchGap := float32(8)

			totalSwatchWidth := swatchW*float32(swCols) + swatchGap*float32(swCols-1)
			startX := cursor[0] + (w-totalSwatchWidth)/2

			bgP0 := cursor
			bgP1 := math.Add2f(bgP0, [2]float32{w, 0})
			bgP2 := math.Add2f(bgP1, [2]float32{0, -float32(swRows) * swH})
			bgP3 := math.Add2f(bgP0, [2]float32{0, -float32(swRows) * swH})
			trid.AddQuad(bgP0, bgP1, bgP2, bgP3, bButton.ScaleRGB(colors.popup.backgroundBlack))

			type swatchInfo struct {
				c   CRRColor
				box math.Extent2D
			}
			var swatches []swatchInfo

			for i, c := range swColors {
				rowIdx := i / swCols
				colIdx := i % swCols
				x0 := startX + float32(colIdx)*(swatchW+swatchGap)
				yTop := cursor[1] - float32(rowIdx)*swH
				yOffset := (swH - swatchH) / 2
				sp0 := [2]float32{x0, yTop - yOffset}
				sp1 := [2]float32{x0 + swatchW, yTop - yOffset}
				sp2 := [2]float32{x0 + swatchW, yTop - yOffset - swatchH}
				sp3 := [2]float32{x0, yTop - yOffset - swatchH}
				trid.AddQuad(sp0, sp1, sp2, sp3, c.BrightRGB(radar.Brightness(ps.CRR.ColorBright[c])))
				if ps.CRR.SelectedColor == c {
					ld.AddLineLoop(bBorder.ScaleRGB(colors.crr.swatchHighlight), [][2]float32{sp0, sp1, sp2, sp3})
				}
				swatches = append(swatches, swatchInfo{
					c:   c,
					box: math.Extent2D{P0: [2]float32{sp3[0], sp2[1]}, P1: [2]float32{sp1[0], sp0[1]}},
				})
			}
			cursor = math.Add2f(cursor, [2]float32{0, -float32(swRows) * swH})

			// Group label rows
			dimColor := bBorder.ScaleRGB(colors.menu.rowDimOutline)
			brightColor := bBorder.ScaleRGB(colors.menu.rowHoverOutline)

			groupExtents := make(map[string]math.Extent2D)
			for _, l := range groupLabels {
				rp0 := cursor
				rp1 := math.Add2f(cursor, [2]float32{w, 0})
				rp2 := math.Add2f(rp1, [2]float32{0, -itemH})
				rp3 := math.Add2f(rp0, [2]float32{0, -itemH})
				trid.AddQuad(rp0, rp1, rp2, rp3, bButton.ScaleRGB(colors.popup.backgroundBlack))
				style := renderer.TextStyle{Font: font,
					Color: bText.ScaleRGB(ep.CRRGroups[l].Color.BrightRGB(ps.CRR.ColorBright[ep.CRRGroups[l].Color]))}
				labelText := strings.ToUpper(l)
				th := font.LayoutBounds(labelText, 0).Height()
				textY := rp0[1] - itemH/2 + th/2
				td.AddText(labelText, [2]float32{rp0[0] + 4, textY}, style)
				cursor = rp3
				extent := math.Extent2D{P0: rp3, P1: rp1}
				groupExtents[l] = extent

				hovered := mouse != nil && extent.Inside(mouse.Pos)
				if hovered {
					ld.AddLine(rp0, rp1, brightColor)
					ld.AddLine(rp3, rp2, brightColor)
					ld.AddLine(rp0, rp3, brightColor)
					ld.AddLine(rp1, rp2, brightColor)
				} else {
					ld.AddLine(rp3, rp2, dimColor)
				}
			}

			// Click handling for swatches and group labels
			if ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse) {
				for _, s := range swatches {
					if mouse != nil && s.box.Inside(mouse.Pos) {
						ps.CRR.SelectedColor = s.c
						return cursor
					}
				}
				for _, l := range groupLabels {
					if mouse != nil && groupExtents[l].Inside(mouse.Pos) {
						if g := ep.CRRGroups[l]; g != nil {
							g.Color = ps.CRR.SelectedColor
						}
						return cursor
					}
				}
			}

			return cursor
		},
	}

	ep.DrawERAMMenu(ctx, transforms, cb, origin, cfg)
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
	for l, g := range util.SortedMap(ep.CRRGroups) {
		if g == nil {
			continue
		}
		// Get the color for the CRR fix
		fixColor := g.Color.BrightRGB(ps.CRR.ColorBright[g.Color])
		style := renderer.TextStyle{Font: font, Color: fixColor}
		p := transforms.WindowFromLatLongP(g.Location)
		// Draw asterisk then label with a space
		td.AddText("*", p, style)
		ax := font.LayoutBounds("*", style.LineSpacing).Width()
		lp := math.Add2f(p, [2]float32{ax + 4, 0})
		label := strings.ToUpper(g.Label)
		td.AddText(label, lp, style)
		ext := font.LayoutBounds("* "+label, style.LineSpacing)
		// P0 = min corner, P1 = max corner for Inside to work
		ep.crrFixRects[l] = math.Extent2D{
			P0: [2]float32{p[0], p[1] - ext.Height()}, // bottom-left (min)
			P1: [2]float32{p[0] + ext.Width(), p[1]},  // top-right (max)
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)

	// Handle click to seed LF if input is empty
	if mouse := ctx.Mouse; (ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse)) && len(ep.Input) == 0 {
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
func (ep *ERAMPane) drawCRRDistances(ctx *panes.Context, tracks []sim.Track, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if ep.CRRGroups == nil || len(ep.CRRGroups) == 0 {
		return
	}

	// Build a map of callsign -> (group, distance)
	type crrEntry struct {
		group  *CRRGroup
		distNM float32
	}
	acCRR := make(map[av.ADSBCallsign]crrEntry)

	for _, trk := range tracks {
		for _, g := range ep.CRRGroups {
			if _, ok := g.Aircraft[trk.ADSBCallsign]; ok {
				trkState := ep.TrackState[trk.ADSBCallsign]
				dist := math.NMDistance2LL(trkState.Track.Location, g.Location)
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

	for _, trk := range tracks {
		entry, ok := acCRR[trk.ADSBCallsign]
		if !ok {
			continue
		}

		state := ep.TrackState[trk.ADSBCallsign]
		if state == nil {
			continue
		}

		// Get position below the track target
		location := state.Track.Location
		trackWin := transforms.WindowFromLatLongP(location)

		// Position the distance text below and to the left of the track
		distStr := fmt.Sprintf("%.1f", entry.distNM)
		dext := font.LayoutBounds(distStr, 0)

		// Position offset: below and left of the track symbol
		pos := [2]float32{
			trackWin[0] - dext.Width() - 8,
			trackWin[1] - dext.Height() - 4,
		}

		// Use the CRR group color
		color := entry.group.Color.BrightRGB(ps.CRR.ColorBright[entry.group.Color])

		td.AddText(distStr, pos, renderer.TextStyle{
			Font:        font,
			Color:       color,
			LineSpacing: 0,
		})
	}

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

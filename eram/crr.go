// eram/crr.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
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
func (c CRRColor) BrightRGB(b scope.Brightness) renderer.RGB {
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

// crrContentChars is the width of the CRR view's contents in characters:
// an entry's non-ADS-B indicator column, ACID, and distance.
const crrContentChars = 20

// drawCRRView renders the Continuous Range Readout view.
func (ep *Scope) drawCRRView(ctx *scope.Context, tracks []sim.Track, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if !ps.CRR.Visible {
		return
	}
	if ep.CRRGroups == nil {
		ep.CRRGroups = make(map[string]*CRRGroup)
	}

	font := ep.ERAMFont(ps.CRR.Font)
	// The same width as a list mode column.
	width := float32(crrContentChars)*charWidth(font) + 2*titleBarButtonPad

	trackPos := make(map[av.ADSBCallsign]math.Point2LL)
	for _, trk := range tracks {
		trackPos[trk.ADSBCallsign] = ep.TrackState[trk.ADSBCallsign].Track.Location
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
		Menu:       crrMenu{},
		OnMinimize: func() { ps.CRR.Visible = false },
	}
	switch {
	case len(labels) == 0:
		ep.crrLabelRects = nil
	case !ps.CRR.ListMode:
		v.BodyHeight, v.DrawBody = ep.buildCRRPanel(labels, font)
	default:
		v.RowSource = ep.buildCRRList(labels, trackPos)
	}
	ep.DrawView(ctx, transforms, cb, v)

	// The Location Group Panel's labels take the clicks the list's group
	// headers do.
	mouse := ctx.Mouse
	if (!ep.mousePrimaryClicked(mouse) && !ep.mouseTertiaryClicked(mouse)) || ep.popupCovers(mouse.Pos) {
		return
	}
	for label, rect := range ep.crrLabelRects {
		if rect.Inside(mouse.Pos) {
			ep.clickCRRGroup(label, ep.mouseTertiaryClicked(mouse))
			ep.consumeMouseClick(mouse)
			return
		}
	}
}

// clickCRRGroup handles a click on a group's label in the view: a TBP
// starts an LF message for the group, and a TBE empties it or, if it is
// empty, deletes it.
func (ep *Scope) clickCRRGroup(label string, tertiary bool) {
	if !tertiary {
		ep.input.Set("LF " + strings.ToUpper(label) + " ")
		return
	}
	if g := ep.CRRGroups[label]; g != nil {
		if len(g.Aircraft) > 0 {
			g.Aircraft = make(map[av.ADSBCallsign]struct{})
		} else {
			delete(ep.CRRGroups, label)
		}
	}
}

// buildCRRPanel constructs the panel-mode body: horizontal row of buttons,
// one per group. Body height = button height + small top margin.
func (ep *Scope) buildCRRPanel(labels []string, font *renderer.Font) (float32, func(math.Extent2D, *ViewBuilders)) {
	ps := ep.currentPrefs()
	bodyHeight := font.LayoutBounds("X", 0).Height() + 8 + 4

	return bodyHeight, func(body math.Extent2D, b *ViewBuilders) {
		ep.crrLabelRects = make(map[string]math.Extent2D)
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

// buildCRRList constructs the list-mode RowSource: each group's header,
// which takes the clicks a Location Group Panel label does, above its
// aircraft in distance order, each of which a pick removes from the group,
// with 2px spacers between groups. Group-header rows set Color explicitly;
// aircraft rows leave Color zero and let the View fill it.
func (ep *Scope) buildCRRList(labels []string, trackPos map[av.ADSBCallsign]math.Point2LL) *ViewRowSource {
	ps := ep.currentPrefs()

	var rows []Row
	for _, label := range labels {
		g := ep.CRRGroups[label]
		if g == nil {
			continue
		}
		if len(rows) > 0 {
			rows = append(rows, Row{SpacerHeight: 2})
		}
		rows = append(rows, Row{
			Label:    strings.ToUpper(g.Label),
			Color:    g.Color.BrightRGB(ps.CRR.ColorBright[g.Color]),
			Centered: true,
			OnClick:  func(c RowClick) { ep.clickCRRGroup(label, c.Tertiary) },
		})

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
		slices.SortFunc(entries, func(a, b dist) int { return cmp.Compare(a.d, b.d) })
		for _, e := range entries {
			// Entries start with a column for the non-ADS-B indicator. The
			// distances are right-aligned so that their decimal points line
			// up (figure 4-174).
			row := Row{
				Label:   fmt.Sprintf("  %-8s%*.1f", e.cs, crrContentChars-10, e.d),
				OnClick: func(RowClick) { delete(g.Aircraft, e.cs) },
			}
			if state := ep.TrackState[e.cs]; ps.NonADSBIndicator && state != nil && nonADSB(state.Track) {
				row.AfterDraw = ep.nonADSBRowMark(ep.ERAMFont(ps.CRR.Font))
			}
			rows = append(rows, row)
		}
	}

	return &ViewRowSource{
		Rows:         rows,
		FontIndex:    ps.CRR.Font,
		ContentChars: crrContentChars,
		MaxCols:      1,
		VisibleRows:  visibleLines(ps.CRR.Lines, len(rows)),
		RowSpacing:   RowSpacingCompact,
		ScrollState:  &ep.crrScroll,
	}
}

// crrMenu is the CRR view's menu (figure 4-175): the view's settings, the
// Range Color Selection panel, and the groups, a pick on one of which gives
// it the selected color (4.2.11.5).
type crrMenu struct{}

func (crrMenu) title() string { return "CRR" }

func (crrMenu) rows(ep *Scope, _ *scope.Context) []MenuItem {
	ps := ep.currentPrefs()
	rows := []MenuItem{
		ep.makeBooleanMenuItem(&ps.CRR.Opaque, "O", "T"),
		ep.makeToggleMenuItem(&ps.CRR.ShowBorder, "BORDER"),
		makeLinesMenuItem(ep, &ps.CRR.Lines, 3),
		makeIntMenuItem(ep, &ps.CRR.Font, "FONT", 1, 3, 1),
		makeIntMenuItem(ep, &ps.CRR.Bright, "BRIGHT", 0, 100, 2),
		ep.makeToggleMenuItem(&ps.CRR.ListMode, "LIST"),
		{Label: fmt.Sprintf("COLOR %d", ps.CRR.ColorBright[ps.CRR.SelectedColor]), BgColor: colors.popup.backgroundBlack,
			Color: CRRGreen.BrightRGB(90), OnClick: func(click MenuClickType) bool {
				v := ps.CRR.ColorBright[ps.CRR.SelectedColor]
				adjustMenuValue(ep, &v, click, 0, 100, 2)
				ps.CRR.ColorBright[ps.CRR.SelectedColor] = v
				return false
			}},
		{Custom: &MenuCustomRow{Draw: ep.drawCRRSwatches}},
	}
	for _, l := range util.SortedMapKeys(ep.CRRGroups) {
		g := ep.CRRGroups[l]
		rows = append(rows, MenuItem{
			Label:   strings.ToUpper(l),
			BgColor: colors.popup.backgroundBlack,
			Color:   g.Color.BrightRGB(ps.CRR.ColorBright[g.Color]),
			OnClick: func(MenuClickType) bool {
				g.Color = ps.CRR.SelectedColor
				return false
			},
		})
	}
	return rows
}

// drawCRRSwatches draws the Range Color Selection panel: a swatch of each
// color, two to a row, the selected one outlined. A click on a swatch
// selects its color.
func (ep *Scope) drawCRRSwatches(cursor [2]float32, w float32, b *ViewBuilders, mouse *platform.MouseState) float32 {
	ps := ep.currentPrefs()
	const cols = 2
	const swatchW, swatchH, gap = float32(60), float32(12), float32(8)
	swatchColors := []CRRColor{CRRGreen, CRRMagenta, CRRWhite, CRRAmber}
	h := float32((len(swatchColors)+cols-1)/cols) * viewPopupItemH

	addQuad(b.Trid, math.Extent2D{P0: [2]float32{cursor[0], cursor[1] - h}, P1: [2]float32{cursor[0] + w, cursor[1]}},
		ps.Brightness.Button.ScaleRGB(colors.popup.backgroundBlack))
	startX := cursor[0] + (w-(cols*swatchW+(cols-1)*gap))/2
	clicked := ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse)
	for i, c := range swatchColors {
		x := startX + float32(i%cols)*(swatchW+gap)
		top := cursor[1] - float32(i/cols)*viewPopupItemH - (viewPopupItemH-swatchH)/2
		s := math.Extent2D{P0: [2]float32{x, top - swatchH}, P1: [2]float32{x + swatchW, top}}
		addQuad(b.Trid, s, c.BrightRGB(ps.CRR.ColorBright[c]))
		if ps.CRR.SelectedColor == c {
			b.Ld.AddLineLoop(ps.Brightness.Border.ScaleRGB(colors.crr.swatchHighlight), s.Corners())
		}
		if clicked && s.Inside(mouse.Pos) {
			ps.CRR.SelectedColor = c
		}
	}
	return h
}

// drawCRRFixes draws clickable CRR fix labels when enabled under ATC TOOLS.
func (ep *Scope) drawCRRFixes(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
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
	if mouse := ctx.Mouse; (ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse)) && len(ep.input) == 0 {
		for id, ex := range ep.crrFixRects {
			if ex.Inside(mouse.Pos) {
				ep.input.Set("LF " + strings.ToUpper(id) + " ")
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
// CRR group the aircraft belongs to. These are the range data blocks, which
// CRR RDB displays and suppresses (4.2.11.6.1).
func (ep *Scope) drawCRRDistances(ctx *scope.Context, tracks []sim.Track, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if len(ep.CRRGroups) == 0 || !ps.CRR.RDBs {
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

	font := ep.ERAMFont(ps.RDBSize)

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
		color := entry.group.Color.BrightRGB(ps.CRR.ColorBright[entry.group.Color] + ep.dwellBrightness(trk))

		td.AddText(distStr, pos, renderer.TextStyle{
			Font:        font,
			Color:       color,
			LineSpacing: 0,
		})
	}

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

///////////////////////////////////////////////////////////////////////////
// LF - CRR (Continuous Range Readout) Handlers

func handleCRRCreateWithAircraft(ep *Scope, ctx *scope.Context, loc CRRLocation, label string, tracks []*sim.Track) (CommandStatus, error) {
	if !validCRRLabel(label) {
		return CommandStatus{}, NewError("REJECT - CRR - GROUP NOT\nFOUND\nCONT RANGE\nLF %s %s", loc.Token, label)
	}

	// Check if group already exists
	if ep.CRRGroups == nil {
		ep.CRRGroups = make(map[string]*CRRGroup)
	}
	if _, ok := ep.CRRGroups[label]; ok {
		return CommandStatus{}, NewError("REJECT - CRR - GROUP LABEL\n ALREADY EXISTS\nCONT RANGE\nLF %s %s", loc.Token, label)
	}

	// All tracks must be associated
	for _, trk := range tracks {
		if !trk.IsAssociated() {
			return CommandStatus{}, ErrIllegalACID
		}
	}

	// Create group
	g := &CRRGroup{
		Label:    label,
		Location: loc.Location,
		Color:    ep.currentPrefs().CRR.SelectedColor,
		Aircraft: make(map[av.ADSBCallsign]struct{}),
	}
	ep.CRRGroups[label] = g

	for _, trk := range tracks {
		g.Aircraft[trk.ADSBCallsign] = struct{}{}
	}

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", fmt.Sprintf("CRR GROUP %s CREATED", label)},
	}, nil
}

func handleCRRCreate(ep *Scope, ctx *scope.Context, loc CRRLocation, label string) (CommandStatus, error) {
	return handleCRRCreateWithAircraft(ep, ctx, loc, label, nil)
}

func handleCRRCreateAutoLabel(ep *Scope, ctx *scope.Context, loc CRRLocation) (CommandStatus, error) {
	// Auto-derive label from fix name if it's a valid fix
	label := loc.Token
	// Check if it's a valid fix name that can be used as a label
	if _, ok := ctx.Client.State.Locate(label); ok && validCRRLabel(label) {
		return handleCRRCreate(ep, ctx, loc, label)
	}
	return CommandStatus{}, NewError("REJECT - MESSAGE TOO SHORT\nCONT RANGE\nLF //%s", loc.Token)
}

// handleCRRCreateClicked creates a group at the picked point, with up to
// four flights in it; it takes a label, since a point has no fix name to
// label it with (4.2.11.4.1, 4.2.11.4.2).
func handleCRRCreateClicked(ep *Scope, ctx *scope.Context, loc math.Point2LL, label string,
	tracks []*sim.Track) (CommandStatus, error) {
	return handleCRRCreateWithAircraft(ep, ctx, CRRLocation{Location: loc, Token: locationSymbol}, label, tracks)
}

// handleCRRWrongOrder handles LF LABEL {pos} - wrong order error
func handleCRRWrongOrder(label string, pos math.Point2LL) (CommandStatus, error) {
	return CommandStatus{}, NewError("REJECT - CRR - GROUP NOT\nFOUND\nCONT RANGE\nLF %s %s", strings.ToUpper(label), locationSymbol)
}

func handleCRRToggleMembership(ep *Scope, label string, tracks []*sim.Track) (CommandStatus, error) {
	if !validCRRLabel(label) {
		return CommandStatus{}, ErrCommandFormat
	}

	// Find existing group
	g := ep.CRRGroups[label]
	if g == nil {
		return CommandStatus{}, ErrCommandFormat
	}

	// Toggle membership for each aircraft
	for _, trk := range tracks {
		if !trk.IsAssociated() {
			return CommandStatus{}, ErrIllegalACID
		}

		cs := trk.ADSBCallsign
		if _, ok := g.Aircraft[cs]; ok {
			delete(g.Aircraft, cs)
		} else {
			g.Aircraft[cs] = struct{}{}
		}
	}

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", fmt.Sprintf("CRR UPDATED %s", g.Label)},
	}, nil
}

func handleCRRLabelOnly(label string) (CommandStatus, error) {
	// LF LABEL without click or aircraft - group not found
	return CommandStatus{}, NewError("REJECT - CRR - GROUP NOT\nFOUND\nCONT RANGE\nLF %s", strings.ToUpper(label))
}

func handleCRREmpty() (CommandStatus, error) {
	// LF alone - message too short
	return CommandStatus{}, NewError("REJECT - MESSAGE TOO SHORT\nCONT RANGE\nLF")
}

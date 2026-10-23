// eram/saa.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"cmp"
	"slices"
	"strings"

	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/util"
)

// Special activity airspace (SAA): the SAA FILTER view and the areas it
// displays on the scope (4.2.18). The SAAs are the special use airspace
// from the CIFP within the ARTCC, as the extent of its airports
// approximates it. The CIFP gives no schedules, so the areas
// it marks as always active are hot and the others are cold; none is ever
// pending. Nor does it give the adaptation that forces an SAA to the
// display, so every SAA is "Not Forced".

// saa is a special activity airspace.
type saa struct {
	id        string // its CIFP type and designation, which key its settings
	label     string
	hot       bool
	volumes   []db.SpecialUseAirspaceVolume
	altitudes []string      // its volumes' altitude ranges, the highest floor first
	labelAt   math.Point2LL // the default label location: below its lower left corner
}

// SAASettings are an SAA's display filters (4.2.18.2): which of its
// boundary (B), altitudes (A), and label (L) are displayed while it is
// inactive and while it is active, and whether DISPLAY, the altitude
// override, is on.
type SAASettings struct {
	Cold, Hot string
	Display   bool
}

var defaultSAASettings = SAASettings{Hot: "BAL"}

// The filter settings that picks on the COLD and HOT pick areas cycle
// through.
var (
	saaColdFilters = []string{"", "B", "BL"}
	saaHotFilters  = []string{"", "B", "BA", "BAL"}
)

func (p *Preferences) saaSettings(id string) SAASettings {
	if s, ok := p.SAAs[id]; ok {
		return s
	}
	return defaultSAASettings
}

func (p *Preferences) setSAASettings(id string, s SAASettings) {
	if p.SAAs == nil {
		p.SAAs = make(map[string]SAASettings)
	}
	p.SAAs[id] = s
}

// filter returns which parts of the SAA are displayed: those of its cold
// or hot filter setting, as it is inactive or active. An active SAA is
// displayed only if it is altitude eligible, overlapping the altitude range
// of the user's airspace, unless DISPLAY is on. DISPLAY also displays the
// boundary and altitudes of an active SAA whose hot setting displays
// nothing.
func (a *saa) filter(s SAASettings, altitudeRange [2]int) string {
	switch {
	case !a.hot:
		return s.Cold
	case s.Hot == "" && s.Display:
		return "BA"
	case s.Display || a.altitudeEligible(altitudeRange):
		return s.Hot
	default:
		return ""
	}
}

func (a *saa) altitudeEligible(altitudeRange [2]int) bool {
	return slices.ContainsFunc(a.volumes, func(v db.SpecialUseAirspaceVolume) bool {
		return v.Floor < altitudeRange[1] && v.Ceiling > altitudeRange[0]
	})
}

// makeSAAs finds the SAAs: the special use airspace within the extent of
// the ARTCC's airports.
func (ep *Scope) makeSAAs(ss client.SimState) {
	ep.saas = nil
	artcc := db.DB.ARTCCForFacility(ss.Facility)
	extent := math.EmptyExtent2D()
	for _, ap := range db.DB.Airports {
		if ap.ARTCC == artcc {
			extent = math.Union(extent, ap.Location)
		}
	}
	if extent.IsEmpty() {
		return
	}
	for _, sua := range db.DB.SpecialUseAirspace {
		if slices.ContainsFunc(sua.Volumes, func(v db.SpecialUseAirspaceVolume) bool {
			return v.PolygonBounds != nil && math.Overlaps(extent, *v.PolygonBounds)
		}) {
			ep.saas = append(ep.saas, makeSAA(sua))
		}
	}
	// The active ones come first.
	slices.SortFunc(ep.saas, func(a, b saa) int {
		if a.hot != b.hot {
			return util.Select(a.hot, -1, 1)
		}
		return cmp.Compare(a.label, b.label)
	})
}

func makeSAA(sua db.SpecialUseAirspace) saa {
	a := saa{
		id:      string(rune(sua.Type)) + sua.Designation,
		label:   sua.Designation,
		hot:     slices.ContainsFunc(sua.Volumes, func(v db.SpecialUseAirspaceVolume) bool { return v.ActiveContinuously }),
		volumes: sua.Volumes,
	}
	switch sua.Type {
	case db.MilitaryOperationsArea:
		a.label += " MOA"
	case db.UnspecifiedArea:
	default:
		a.label = string(rune(sua.Type)) + "-" + a.label
	}
	if i := slices.IndexFunc(sua.Volumes, func(v db.SpecialUseAirspaceVolume) bool { return v.Description != "" }); i >= 0 {
		a.label = sua.Volumes[i].Description
	}

	bounds := math.EmptyExtent2D()
	vols := slices.Clone(sua.Volumes)
	slices.SortStableFunc(vols, func(a, b db.SpecialUseAirspaceVolume) int { return cmp.Compare(b.Floor, a.Floor) })
	for _, v := range vols {
		if v.PolygonBounds != nil {
			bounds = bounds.Union(*v.PolygonBounds)
		}
		alt := formatAltitudeBlock([2]int{v.Floor / 100, min(v.Ceiling/100, 999)})
		if !slices.Contains(a.altitudes, alt) {
			a.altitudes = append(a.altitudes, alt)
		}
	}
	a.labelAt = bounds.P0
	return a
}

// saaColor returns the color of an SAA's boundary, label, and altitudes:
// orange while it is active and white while it is inactive (4.2.18.1).
func (ep *Scope) saaColor(a *saa) renderer.RGB {
	ps := ep.currentPrefs()
	return util.Select(a.hot, ps.SAAFilter.Hot.ScaleRGB(colors.saa.hot), ps.SAAFilter.Cold.ScaleRGB(colors.saa.cold))
}

// saaText returns the lines of an SAA's label that its filter displays:
// its label and its altitudes (4.2.18.1.1).
func saaText(a *saa, filter string) []string {
	var lines []string
	if strings.Contains(filter, "L") {
		lines = append(lines, a.label)
	}
	if strings.Contains(filter, "A") {
		lines = append(lines, a.altitudes...)
	}
	return lines
}

///////////////////////////////////////////////////////////////////////////
// The SAAs on the scope

// drawSAAs draws the SAAs' boundaries, labels, and altitudes as their
// filters select, and the boundary of one whose label is being torn off
// (4.2.18.3). A torn-off label is drawn by its tear-off instead.
func (ep *Scope) drawSAAs(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	font := ep.ERAMFont(1)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	altitudeRange := ctx.Client.State.GetAltitudeRange()
	tearingOff, _ := ep.saaBeingTornOff()
	for i := range ep.saas {
		a := &ep.saas[i]
		filter, color := a.filter(ps.saaSettings(a.id), altitudeRange), ep.saaColor(a)
		if strings.Contains(filter, "B") || a.id == tearingOff {
			for _, v := range a.volumes {
				addDashedLoop(ld, util.MapSlice(v.Vertices, transforms.WindowFromLatLongP), color)
			}
		}
		if lines := saaText(a, filter); len(lines) > 0 && !(viewEntry{&saaTearOffs, a.id}).tornOff(ep) {
			td.AddText(strings.Join(lines, "\n"), transforms.WindowFromLatLongP(a.labelAt),
				renderer.TextStyle{Font: font, Color: color})
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

// saaBeingTornOff returns the id of the SAA whose label is being torn off
// or moved, if there is one.
func (ep *Scope) saaBeingTornOff() (string, bool) {
	if d := ep.entryDrag; d != nil && d.entry.tearOffs == &saaTearOffs {
		return d.entry.id, true
	}
	return "", false
}

// saaTearOffs are the tear-offs of the SAAs' labels, by SAA id. One shows
// the label and altitudes that its SAA's filter displays, in the SAA's
// color and without a border, as the label on the map does (4.2.18.1.1); a
// click on it offers SUPPRESS LABEL/ALT, which sets the SAA's current
// filter to its boundary alone (4.2.18.3.1).
var saaTearOffs = entryTearOffs{
	locations: func(ep *Scope) *map[string]math.Point2LL { return &ep.currentPrefs().SAAFilter.TearOffs },
	tearOff: func(ep *Scope, ctx *scope.Context, id string) (entryTearOff, bool) {
		a, ok := ep.saaByID(id)
		if !ok {
			return entryTearOff{}, false
		}
		ps := ep.currentPrefs()
		lines := saaText(a, a.filter(ps.saaSettings(id), ctx.Client.State.GetAltitudeRange()))
		rows := util.MapSlice(lines, func(l string) Row { return Row{Label: l, Color: ep.saaColor(a)} })
		if len(rows) == 0 {
			// A suppressed label's tear-off is its bar alone.
			rows = []Row{{}}
		}
		rows[0].TearOff = id
		return entryTearOff{
			ViewRowSource: ViewRowSource{
				Rows:               rows,
				FontIndex:          1,
				ContentChars:       util.ReduceSlice(lines, 0, func(l string, n int) int { return max(n, len(l)) }),
				ReserveBadgeColumn: true,
				RowSpacing:         RowSpacingCompact,
			},
			brightness: ps.SAAFilter.Bright,
		}, true
	},
	click: func(ep *Scope, ctx *scope.Context, id string, extent math.Extent2D) {
		a, ok := ep.saaByID(id)
		if !ok {
			return
		}
		ps := ep.currentPrefs()
		ep.openPickAreaPopup(ctx, extent, "SUPPRESS LABEL/ALT", func() {
			s := ps.saaSettings(id)
			if a.hot {
				s.Hot = "B"
			} else {
				s.Cold = "B"
			}
			ps.setSAASettings(id, s)
		})
	},
}

func (ep *Scope) saaByID(id string) (*saa, bool) {
	if i := slices.IndexFunc(ep.saas, func(a saa) bool { return a.id == id }); i >= 0 {
		return &ep.saas[i], true
	}
	return nil, false
}

// addDashedLoop outlines a polygon with dashes (figure 4-229) that continue
// from one edge to the next, so that edges shorter than a dash, as along
// an arc, don't merge into a solid line.
func addDashedLoop(ld *renderer.ColoredLinesDrawBuilder, pts [][2]float32, color renderer.RGB) {
	const dash, gap = 8, 6
	d := float32(0) // along the current dash or gap
	for i, p0 := range pts {
		p1 := pts[(i+1)%len(pts)]
		l := math.Distance2f(p0, p1)
		for t := float32(0); t < l; {
			run := util.Select(d < dash, dash-d, dash+gap-d)
			step := min(run, l-t)
			if d < dash {
				ld.AddLine(math.Lerp2f(t/l, p0, p1), math.Lerp2f((t+step)/l, p0, p1), color)
			}
			t += step
			d += step
			if d >= dash+gap {
				d = 0
			}
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// The SAA FILTER view

// saaRow is the geometry of an entry in the SAA FILTER view: its COLD and
// HOT filter settings, its DISPLAY button, its tear-off pick area, and
// where its label starts.
type saaRow struct {
	cold, hot, tearOff math.Extent2D
	display            [2]float32 // the button's center
	displayRadius      float32
	label              [2]float32
}

// saaRowAt lays out the entry whose row has its top left at (x0, y). From
// left to right, it has the COLD and HOT filter settings' boxes, the
// DISPLAY button, the tear-off pick area, and the label, each one padded
// from the one before it and centered vertically in the row.
func saaRowAt(x0, y, rowH float32, font *renderer.Font) saaRow {
	pad := float32(titleBarButtonPad)
	midY := y - rowH/2
	// centered returns the extent of something w wide and h high that has
	// its left edge at x and is centered vertically in the row.
	centered := func(x, w, h float32) math.Extent2D {
		return math.Extent2D{P0: [2]float32{x, midY - h/2}, P1: [2]float32{x + w, midY + h/2}}
	}

	var r saaRow
	// The filter settings' boxes fit four characters and leave 2 pixels
	// above and below them.
	boxW, boxH := 4*charWidth(font)+2*pad, rowH-4
	r.cold = centered(x0+pad, boxW, boxH)
	r.hot = centered(r.cold.P1[0]+pad, boxW, boxH)
	// The DISPLAY button's circle leaves 3 pixels above and below it.
	r.displayRadius = (rowH - 6) / 2
	r.display = [2]float32{r.hot.P1[0] + 2*pad + r.displayRadius, midY}
	sq := cellHeight(font) * 0.6
	r.tearOff = centered(r.display[0]+r.displayRadius+2*pad, sq, sq)
	// label is the top left of the label's text.
	r.label = [2]float32{r.tearOff.P1[0] + pad, midY + cellHeight(font)/2}
	return r
}

// drawSAAFilterView draws the SAA FILTER view, which lists the SAAs a page
// at a time; picks on an entry's COLD and HOT settings cycle through them,
// one on its DISPLAY button toggles it, and one on its tear-off pick area
// starts tearing off its label, which the next click places (4.2.18).
// Settings can't be changed while a label is being torn off (4.2.18.3).
func (ep *Scope) drawSAAFilterView(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if !ps.SAAFilter.Visible {
		return
	}
	font := ep.ERAMFont(ps.SAAFilter.Font)
	cw := charWidth(font)
	rowH := lineHeight(font) + 6

	lines := visibleLines(ps.SAAFilter.Lines, len(ep.saas))
	pages := max(1, (len(ep.saas)+lines-1)/lines)
	ep.saaScroll.Offset = math.Clamp(ep.saaScroll.Offset, 0, pages-1)
	shown := ep.saas[min(len(ep.saas), ep.saaScroll.Offset*lines):min(len(ep.saas), (ep.saaScroll.Offset+1)*lines)]

	proto := saaRowAt(0, 0, rowH, font)
	maxLabel := util.ReduceSlice(ep.saas, len("DISPLAY"), func(a saa, n int) int { return max(n, len(a.label)) })
	width := max(proto.label[0], proto.display[0]-proto.displayRadius) + float32(maxLabel)*cw + titleBarButtonPad

	var body math.Extent2D
	v := View{
		Prefs:      &ps.SAAFilter.ViewPreferences,
		ID:         "saa-filter",
		Width:      width,
		BodyHeight: float32(1+lines) * rowH,
		BodyFont:   font,
		Title:      "SAA FILTER",
		Menu:       saaFilterMenu{},
		DrawBody: func(e math.Extent2D, b *ViewBuilders) {
			body = e
			ep.drawSAAFilterBody(e, b, shown, font, rowH)
		},
	}
	if pages > 1 {
		v.Scroll = &ViewScrollConfig{State: &ep.saaScroll, MaxOffset: pages - 1}
	}
	ep.DrawView(ctx, transforms, cb, v)

	mouse := ctx.Mouse
	if _, tearingOff := ep.saaBeingTornOff(); tearingOff ||
		(!ep.mousePrimaryClicked(mouse) && !ep.mouseTertiaryClicked(mouse)) || ep.popupCovers(mouse.Pos) {
		return
	}
	for i, a := range shown {
		r := saaRowAt(body.P0[0], body.P1[1]-float32(i+1)*rowH, rowH, font)
		s := ps.saaSettings(a.id)
		entry := viewEntry{&saaTearOffs, a.id}
		switch {
		case r.cold.Inside(mouse.Pos):
			s.Cold = nextSAAFilter(saaColdFilters, s.Cold)
		case r.hot.Inside(mouse.Pos):
			s.Hot = nextSAAFilter(saaHotFilters, s.Hot)
		case math.Distance2f(mouse.Pos, r.display) <= r.displayRadius:
			s.Display = !s.Display
		case r.tearOff.Expand(2).Inside(mouse.Pos) && !entry.tornOff(ep):
			ep.startEntryDrag(ctx, entry, [2]float32{r.tearOff.P0[0], r.tearOff.P1[1]})
		default:
			continue
		}
		ps.setSAASettings(a.id, s)
		ep.consumeMouseClick(mouse)
		return
	}
}

func nextSAAFilter(cycle []string, f string) string {
	return cycle[(slices.Index(cycle, f)+1)%len(cycle)]
}

// drawSAAFilterBody draws the column headers and the entries in shown
// (figure 4-225).
func (ep *Scope) drawSAAFilterBody(body math.Extent2D, b *ViewBuilders, shown []saa, font *renderer.Font, rowH float32) {
	ps := ep.currentPrefs()
	text := ps.SAAFilter.Bright.ScaleRGB(colors.view.text)
	hotFilter := ps.SAAFilter.Color.ScaleRGB(colors.saa.hot)
	button := func(c renderer.RGB) renderer.RGB { return ps.Brightness.Button.ScaleRGB(c) }
	outline := ps.Brightness.Border.ScaleRGB(colors.view.buttonOutline)

	header := saaRowAt(body.P0[0], body.P1[1], rowH, font)
	b.Td.AddText("COLD", [2]float32{header.cold.P0[0], header.label[1]}, renderer.TextStyle{Font: font, Color: text})
	b.Td.AddText("HOT", [2]float32{header.hot.P0[0], header.label[1]}, renderer.TextStyle{Font: font, Color: hotFilter})
	b.Td.AddText("DISPLAY", [2]float32{header.display[0] - header.displayRadius, header.label[1]},
		renderer.TextStyle{Font: font, Color: text})

	for i := range shown {
		a := &shown[i]
		s := ps.saaSettings(a.id)
		r := saaRowAt(body.P0[0], body.P1[1]-float32(i+1)*rowH, rowH, font)
		for _, f := range []struct {
			box   math.Extent2D
			value string
			color renderer.RGB
		}{{r.cold, s.Cold, text}, {r.hot, s.Hot, hotFilter}} {
			if f.value != "" {
				addQuad(b.Trid, f.box, button(colors.popup.backgroundGrey))
			}
			b.Ld.AddLineLoop(outline, f.box.Corners())
			b.Td.AddTextCentered(f.value, f.box.Center(), renderer.TextStyle{Font: font, Color: f.color})
		}
		if s.Display {
			b.Trid.AddCircle(r.display, r.displayRadius, 24, button(colors.popup.backgroundGrey))
		}
		b.Ld.AddCircle(r.display, r.displayRadius, 24, outline)
		torn := (viewEntry{&saaTearOffs, a.id}).tornOff(ep)
		addQuad(b.Trid, r.tearOff, button(util.Select(torn, colors.toolbar.tearoffBarTorn, colors.toolbar.tearoffBar)))
		b.Td.AddText(a.label, r.label, renderer.TextStyle{Font: font, Color: ep.saaColor(a)})
	}
}

// saaFilterMenu is the SAA FILTER view's menu (4.2.18.4). It has no PEND
// FONT or PEND brightness, since no SAA is ever pending.
type saaFilterMenu struct{}

func (saaFilterMenu) title() string { return "SAA FIL" }

func (saaFilterMenu) rows(ep *Scope, _ *scope.Context) []MenuItem {
	ps := ep.currentPrefs()
	return []MenuItem{
		ep.makeBooleanMenuItem(&ps.SAAFilter.Opaque, "O", "T"),
		ep.makeToggleMenuItem(&ps.SAAFilter.ShowBorder, "BORDER"),
		makeLinesMenuItem(ep, &ps.SAAFilter.Lines, 1),
		makeIntMenuItem(ep, &ps.SAAFilter.Font, "FONT", 1, 3, 1),
		makeIntMenuItem(ep, &ps.SAAFilter.Bright, "BRIGHT", 0, 100, 2),
		makeIntMenuItem(ep, &ps.SAAFilter.Color, "COLOR", 0, 100, 2),
		makeIntMenuItem(ep, &ps.SAAFilter.Hot, "HOT", 0, 100, 2),
		makeIntMenuItem(ep, &ps.SAAFilter.Cold, "COLD", 0, 100, 2),
	}
}

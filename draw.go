// draw.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	_ "embed"
	"fmt"
	"unsafe"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/mmp/imgui-go/v4"
)

// efficient mapping from names to offsets
type ColorMap struct {
	m   map[string]int
	ids []int
}

func MakeColorMap() ColorMap { return ColorMap{m: make(map[string]int)} }

func (c *ColorMap) Add(name string) {
	if id, ok := c.m[name]; ok {
		c.ids = append(c.ids, id)
	} else {
		newId := len(c.m) + 1
		c.m[name] = newId
		c.ids = append(c.ids, newId)
	}
}

// returns indices of all ids that correspond
func (c *ColorMap) Visit(name string, callback func(int)) {
	if matchId, ok := c.m[name]; !ok {
		return
	} else {
		for i, id := range c.ids {
			if id == matchId {
				callback(i)
			}
		}
	}
}

type PointsDrawable struct {
	size    float32
	p       [][2]float32
	color   []RGB
	indices []int32
}

func (p *PointsDrawable) Reset() {
	p.size = 1
	p.p = p.p[:0]
	p.color = p.color[:0]
	p.indices = p.indices[:0]
}

func (p *PointsDrawable) AddPoint(pt [2]float32, color RGB) {
	p.p = append(p.p, pt)
	p.color = append(p.color, color)
	p.indices = append(p.indices, int32(len(p.p)-1))
}

func (p *PointsDrawable) Bounds() Extent2D {
	return Extent2DFromPoints(p.p)
}

type LinesDrawable struct {
	width   float32
	p       [][2]float32
	color   []RGB
	indices []int32
}

func (l *LinesDrawable) Reset() {
	l.width = 1
	l.p = l.p[:0]
	l.color = l.color[:0]
	l.indices = l.indices[:0]
}

func (l *LinesDrawable) AddLine(p0, p1 [2]float32, color RGB) {
	idx := int32(len(l.p))
	l.p = append(l.p, p0, p1)
	l.color = append(l.color, color, color)
	l.indices = append(l.indices, idx, idx+1)
}

func (l *LinesDrawable) AddPolyline(p [2]float32, color RGB, shape [][2]float32) {
	idx := int32(len(l.p))
	for _, delta := range shape {
		pp := add2ll(p, delta)
		l.p = append(l.p, pp)
		l.color = append(l.color, color)
	}
	for i := 0; i < len(shape); i++ {
		l.indices = append(l.indices, idx+int32(i), idx+int32((i+1)%len(shape)))
	}
}

func (l *LinesDrawable) Bounds() Extent2D {
	return Extent2DFromPoints(l.p)
}

type TrianglesDrawable struct {
	p       [][2]float32
	color   []RGB
	indices []int32
}

func (t *TrianglesDrawable) Reset() {
	t.p = t.p[:0]
	t.color = t.color[:0]
	t.indices = t.indices[:0]
}

func (t *TrianglesDrawable) AddTriangle(p0, p1, p2 [2]float32, color RGB) {
	idx := int32(len(t.p))
	t.p = append(t.p, p0, p1, p2)
	t.color = append(t.color, color, color, color)
	t.indices = append(t.indices, idx, idx+1, idx+2)
}

func (t *TrianglesDrawable) AddQuad(p0, p1, p2, p3 [2]float32, color RGB) {
	idx := int32(len(t.p))
	t.p = append(t.p, p0, p1, p2, p3)
	t.color = append(t.color, color, color, color, color)
	t.indices = append(t.indices, idx, idx+1, idx+2, idx, idx+2, idx+3)
}

func (t *TrianglesDrawable) Bounds() Extent2D {
	return Extent2DFromPoints(t.p)
}

type TextDrawable struct {
	p       [][2]float32 // window coordinates
	uv      [][2]float32
	rgb     []RGB
	indices []int32

	// for background quads
	bgp       [][2]float32
	bgrgb     []RGB
	bgindices []int32
}

type DrawList struct {
	projection, modelview mgl32.Mat4
	clear                 bool
	clearColor            RGB
	scissor               Extent2D

	points []PointsDrawable
	lines  []LinesDrawable
	tris   []TrianglesDrawable
	text   []TextDrawable
}

type DrawStats struct {
	points, lines, tris, chars int
	vertices                   int
	drawCalls                  int
}

func (d *DrawStats) Add(other DrawStats) {
	d.points += other.points
	d.lines += other.lines
	d.tris += other.tris
	d.chars += other.chars
	d.vertices += other.vertices
	d.drawCalls += other.drawCalls
}

// vertices, lines, tris
func (d *DrawList) Stats() (stats DrawStats) {
	for _, l := range d.points {
		stats.vertices += len(l.p)
		stats.points += len(l.indices) / 2
	}
	for _, l := range d.lines {
		stats.vertices += len(l.p)
		stats.lines += len(l.indices) / 2
	}
	for _, t := range d.tris {
		stats.vertices += len(t.p)
		stats.tris += len(t.indices) / 3
	}
	for _, t := range d.text {
		stats.vertices += len(t.p) + len(t.bgp)
		stats.tris += len(t.indices) / 2
		stats.chars += len(t.indices) / 4
	}
	stats.drawCalls = len(d.lines) + len(d.tris) + len(d.text)
	return
}

type TextStyle struct {
	font            *Font
	color           RGB
	lineSpacing     int
	drawBackground  bool
	backgroundColor RGB
}

func (d *DrawList) AddPoints(pd PointsDrawable) {
	if len(pd.p) > 0 {
		d.points = append(d.points, pd)
	}
}

func (d *DrawList) AddLines(ld LinesDrawable) {
	if len(ld.p) > 0 {
		d.lines = append(d.lines, ld)
	}
}

func (d *DrawList) AddLinesWithWidth(ld LinesDrawable, width float32) {
	if len(ld.p) > 0 {
		d.lines = append(d.lines, ld)
		d.lines[len(d.lines)-1].width = width
	}
}

func (d *DrawList) AddTriangles(td TrianglesDrawable) {
	if len(td.p) > 0 {
		d.tris = append(d.tris, td)
	}
}

func (d *DrawList) AddText(td TextDrawable) {
	if len(td.p) > 0 {
		d.text = append(d.text, td)
	}
}

func (d *DrawList) Reset() {
	d.points = d.points[:0]
	d.lines = d.lines[:0]
	d.tris = d.tris[:0]
	d.text = d.text[:0]
}

func (td *TextDrawable) AddTextCentered(text string, p [2]float32, style TextStyle) {
	bx, by := style.font.BoundText(text, 0)
	p[0] -= float32(bx) / 2
	p[1] += float32(by) / 2
	td.AddText(text, p, style)
}

// p is in pane coordinates: (0,0) is lower left corner
func (td *TextDrawable) AddText(s string, p [2]float32, style TextStyle) {
	td.AddTextMulti([]string{s}, p, []TextStyle{style})
}

func (td *TextDrawable) Resize(n int) {
	if cap(td.uv)-len(td.uv) < 4*n {
		alloc := 4 * n
		if alloc < 2*cap(td.uv) {
			alloc = 2 * cap(td.uv)
		}
		s := make([][2]float32, len(td.uv), alloc)
		copy(s, td.uv)
		td.uv = s
	}
	if cap(td.p)-len(td.p) < 4*n {
		alloc := 4 * n
		if alloc < 2*cap(td.p) {
			alloc = 2 * cap(td.p)
		}
		s := make([][2]float32, len(td.p), alloc)
		copy(s, td.p)
		td.p = s
	}
	if cap(td.rgb)-len(td.rgb) < 4*n {
		alloc := 4 * n
		if alloc < 2*cap(td.rgb) {
			alloc = 2 * cap(td.rgb)
		}
		s := make([]RGB, len(td.rgb), alloc)
		copy(s, td.rgb)
		td.rgb = s
	}
	if cap(td.indices)-len(td.indices) < 4*n {
		alloc := 4 * n
		if alloc < 2*cap(td.indices) {
			alloc = 2 * cap(td.indices)
		}
		s := make([]int32, len(td.indices), alloc)
		copy(s, td.indices)
		td.indices = s
	}
}

func (td *TextDrawable) AddTextMulti(text []string, p [2]float32, styles []TextStyle) {
	// Preallocate space if needed
	strlen := 0
	for _, t := range text {
		strlen += len(t)
	}
	td.Resize(strlen)

	// Initial state; start out pixel-perfect, at least
	x0, y0 := float32(int(p[0]+0.5)), float32(int(p[1]+0.5))
	px, py := x0, y0

	for i := range text {
		style := styles[i]

		dy := float32(style.font.size + style.lineSpacing)

		// Bounds for the current line's background box, if needed
		bx0, by0 := px, py

		flushbg := func() {
			bx1, by1 := px, py-dy
			// Add a quad to stake out the background for this line.
			startIdx := int32(len(td.bgp))
			color := style.backgroundColor
			td.bgrgb = append(td.bgrgb, color, color, color, color)
			padx, pady := float32(1), float32(0)
			td.bgp = append(td.bgp, [][2]float32{{bx0 - padx, by0 - pady}, {bx1 + padx, by0 - pady}, {bx1 + padx, by1 + pady}, {bx0 - padx, by1 + pady}}...)
			td.bgindices = append(td.bgindices, startIdx, startIdx+1, startIdx+2, startIdx+3)
		}

		for _, ch := range text[i] {
			glyph := style.font.LookupGlyph(ch)

			if ch == '\n' {
				if style.drawBackground {
					flushbg()
				}

				px = x0
				py -= dy

				bx0, by0 = px, py
				continue
			}

			if glyph.Visible {
				u0, v0, u1, v1 := glyph.U0, glyph.V0, glyph.U1, glyph.V1
				x0, y0, x1, y1 := glyph.X0, glyph.Y0, glyph.X1, glyph.Y1

				// Add the quad for the glyph to the vertex/index buffers
				startIdx := int32(len(td.p))
				td.uv = append(td.uv, [][2]float32{{u0, v0}, {u1, v0}, {u1, v1}, {u0, v1}}...)
				td.rgb = append(td.rgb, style.color, style.color, style.color, style.color)
				td.p = append(td.p, [][2]float32{{px + x0, py - y0}, {px + x1, py - y0}, {px + x1, py - y1}, {px + x0, py - y1}}...)
				td.indices = append(td.indices, startIdx, startIdx+1, startIdx+2, startIdx+3)
			}

			px += glyph.AdvanceX
		}
		if style.drawBackground {
			flushbg()
		}
	}
}

func (td *TextDrawable) Reset() {
	td.p = td.p[:0]
	td.uv = td.uv[:0]
	td.rgb = td.rgb[:0]
	td.indices = td.indices[:0]
	td.bgp = td.bgp[:0]
	td.bgrgb = td.bgrgb[:0]
	td.bgindices = td.bgindices[:0]
}

func (d *DrawList) UseWindowCoordiantes(width float32, height float32) {
	d.projection = mgl32.Ortho2D(0, width, 0, height)
	d.modelview = mgl32.Ident4()
}

///////////////////////////////////////////////////////////////////////////
// ColorScheme

type ColorScheme struct {
	Background RGB
	SplitLine  RGB

	Text          RGB
	TextHighlight RGB
	TextError     RGB

	Safe    RGB
	Caution RGB
	Error   RGB

	// Datablock colors
	SelectedDataBlock   RGB
	UntrackedDataBlock  RGB
	TrackedDataBlock    RGB
	HandingOffDataBlock RGB
	GhostDataBlock      RGB

	Track RGB

	Airport    RGB
	VOR        RGB
	NDB        RGB
	Fix        RGB
	Runway     RGB
	Region     RGB
	SID        RGB
	STAR       RGB
	Geo        RGB
	ARTCC      RGB
	LowAirway  RGB
	HighAirway RGB
	Compass    RGB

	DefinedColors map[string]*RGB
}

func NewColorScheme() *ColorScheme {
	cs := ColorScheme{
		Background: RGB{0, 0, 0},
		SplitLine:  RGB{0.5, 0.5, 0.5},

		Text:          RGB{1, 1, 1},
		TextHighlight: RGB{1, .6, .6},
		TextError:     RGB{1, 0, 0},

		Safe:    RGB{0, 1, 0},
		Caution: RGB{1, 1, 0},
		Error:   RGB{1, 0, 0},

		SelectedDataBlock:   RGB{0, 0.8, 0.8},
		UntrackedDataBlock:  RGB{0, 0, 0.8},
		TrackedDataBlock:    RGB{0, 0.8, 0},
		HandingOffDataBlock: RGB{0.8, 0, 0.8},
		GhostDataBlock:      RGB{0.5, 0.5, 0.5},
		Track:               RGB{1, 1, 1},

		Airport:    RGB{.8, .7, .8},
		VOR:        RGB{.8, .7, .8},
		NDB:        RGB{.8, .7, .8},
		Fix:        RGB{.8, .7, .8},
		Runway:     RGB{.8, .8, .4},
		Region:     RGB{.9, .9, .9},
		Geo:        RGB{.5, .9, .9},
		LowAirway:  RGB{.5, .5, .5},
		HighAirway: RGB{.5, .5, .5},
		SID:        RGB{0, 0, .9},
		STAR:       RGB{.2, .7, .2},
		ARTCC:      RGB{.7, .7, .7},
		Compass:    RGB{.5, .5, .5}}

	cs.DefinedColors = make(map[string]*RGB)
	for name, rgb := range world.sectorFileColors {
		c := rgb
		cs.DefinedColors[name] = &c
	}

	return &cs
}

func (c *ColorScheme) IsDark() bool {
	luminance := 0.2126*c.Background.R + 0.7152*c.Background.G + 0.0722*c.Background.B
	return luminance < 0.35 // ad hoc..
}

func (c *ColorScheme) ShowEditor(handleDefinedColorChange func(string, RGB)) {
	edit := func(uiName string, internalName string, c *RGB) {
		// It's slightly grungy to hide this in here but it does simplify
		// the code below...
		imgui.TableNextColumn()
		ptr := (*[3]float32)(unsafe.Pointer(c))
		flags := imgui.ColorEditFlagsNoAlpha | imgui.ColorEditFlagsNoInputs |
			imgui.ColorEditFlagsRGB | imgui.ColorEditFlagsInputRGB
		if imgui.ColorEdit3V(uiName, ptr, flags) {
			handleDefinedColorChange(internalName, *c)
		}
	}

	names := SortedMapKeys(c.DefinedColors)
	sfdIndex := 0
	sfd := func() bool {
		if sfdIndex < len(names) {
			edit(names[sfdIndex], names[sfdIndex], c.DefinedColors[names[sfdIndex]])
			sfdIndex++
			return true
		} else {
			imgui.TableNextColumn()
			return false
		}
	}

	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg
	if imgui.BeginTableV(fmt.Sprintf("ColorEditor##%p", c), 4, flags, imgui.Vec2{}, 0.0) {
		// TODO: it would be nice to do this in a more data-driven way--to at least just
		// be laying out the table by iterating over slices...
		imgui.TableSetupColumn("General")
		imgui.TableSetupColumn("Aircraft")
		imgui.TableSetupColumn("Radar scope")
		imgui.TableSetupColumn("Sector file definitions")
		imgui.TableHeadersRow()

		imgui.TableNextRow()
		edit("Background", "Background", &c.Background)
		edit("Track", "Track", &c.Track)
		edit("Airport", "Airport", &c.Airport)
		sfd()

		imgui.TableNextRow()
		edit("Text", "Text", &c.Text)
		edit("Tracked data block", "Tracked Data Block", &c.TrackedDataBlock)
		edit("ARTCC", "ARTCC", &c.ARTCC)
		sfd()

		imgui.TableNextRow()
		edit("Highlighted text", "TextHighlight", &c.TextHighlight)
		edit("Selected data block", "Selected Data Block", &c.SelectedDataBlock)
		edit("Compass", "Compass", &c.Compass)
		sfd()

		imgui.TableNextRow()
		edit("Error text", "TextError", &c.TextError)
		edit("Handing-off data block", "HandingOff Data Block", &c.HandingOffDataBlock)
		edit("Fix", "Fix", &c.Fix)
		sfd()

		imgui.TableNextRow()
		edit("Split line", "Split Line", &c.SplitLine)
		edit("Untracked data block", "Untracked Data Block", &c.UntrackedDataBlock)
		edit("Geo", "Geo", &c.Geo)
		sfd()

		imgui.TableNextRow()
		imgui.TableNextColumn()
		edit("Ghost data block", "Ghost Data Block", &c.GhostDataBlock)
		edit("High airway", "HighAirway", &c.HighAirway)
		sfd()

		imgui.TableNextRow()
		imgui.TableNextColumn()
		edit("Safe situation", "Safe", &c.Safe)
		edit("Low airway", "LowAirway", &c.LowAirway)
		sfd()

		imgui.TableNextRow()
		imgui.TableNextColumn()
		edit("Caution situation", "Caution", &c.Caution)
		edit("NDB", "NDB", &c.NDB)
		sfd()

		imgui.TableNextRow()
		imgui.TableNextColumn()
		edit("Error situation", "Error", &c.Error)
		edit("Region", "Region", &c.Region)
		sfd()

		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.TableNextColumn()
		edit("Runway", "Runway", &c.Runway)
		sfd()

		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.TableNextColumn()
		edit("SID", "SID", &c.SID)
		sfd()

		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.TableNextColumn()
		edit("STAR", "STAR", &c.STAR)
		sfd()

		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.TableNextColumn()
		edit("VOR", "VOR", &c.VOR)
		sfd()

		for sfdIndex < len(names) {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TableNextColumn()
			imgui.TableNextColumn()
			sfd()
		}

		imgui.EndTable()
	}
}

// pkg/renderer/builders.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package renderer

import (
	gomath "math"
	"sync"

	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/util"
)

///////////////////////////////////////////////////////////////////////////
// DrawBuilders

// The various *DrawBuilder classes provide capabilities for specifying a
// number of independent things of the same type to draw and then
// generating corresponding buffer storage and draw commands in a
// CommandBuffer. This allows batching up many things to be drawn all in a
// single draw command, with corresponding GPU performance benefits.

// LinesDrawBuilder accumulates lines to be drawn together. Note that it does
// not allow specifying the colors of the lines; instead, whatever the current
// color is (as set via the CommandBuffer SetRGB method) is used when drawing
// them. If per-line colors are required, the ColoredLinesDrawBuilder should be
// used instead.
type LinesDrawBuilder struct {
	p       [][2]float32
	indices []int32
}

// Reset resets the internal arrays used for accumulating lines,
// maintaining the initial allocations.
func (l *LinesDrawBuilder) Reset() {
	l.p = l.p[:0]
	l.indices = l.indices[:0]
}

// AddLine adds a lines with the specified vertex positions to the set of
// lines to be drawn.
func (l *LinesDrawBuilder) AddLine(p0, p1 [2]float32) {
	idx := int32(len(l.p))
	l.p = append(l.p, p0, p1)
	l.indices = append(l.indices, idx, idx+1)
}

// AddLineStrip adds multiple lines to the lines draw builder where each
// line is given by a successive pair of points, a la GL_LINE_STRIP.
func (l *LinesDrawBuilder) AddLineStrip(p [][2]float32) {
	idx := int32(len(l.p))
	l.p = append(l.p, p...)
	for i := 0; i < len(p)-1; i++ {
		l.indices = append(l.indices, idx+int32(i), idx+int32((i+1)))
	}
}

// Adds a line loop, like a line strip but where the last vertex connects
// to the first, a la GL_LINE_LOOP.
func (l *LinesDrawBuilder) AddLineLoop(p [][2]float32) {
	idx := int32(len(l.p))
	l.p = append(l.p, p...)
	for i := range p {
		l.indices = append(l.indices, idx+int32(i), idx+int32((i+1)%len(p)))
	}
}

// AddCircle adds lines that draw the outline of a circle with specified
// and color centered at the specified point p. The nsegs parameter
// specifies the tessellation rate for the circle.
func (l *LinesDrawBuilder) AddCircle(p [2]float32, radius float32, nsegs int) {
	circle := math.CirclePoints(nsegs)

	idx := int32(len(l.p))
	for i := 0; i < nsegs; i++ {
		// Translate the points to be centered around the point p with the
		// given radius and add them to the vertex buffer.
		pi := [2]float32{p[0] + radius*circle[i][0], p[1] + radius*circle[i][1]}
		l.p = append(l.p, pi)
	}
	for i := 0; i < nsegs; i++ {
		// Initialize the index buffer; note that the first vertex is
		// reused as the endpoint of the last line segment.
		l.indices = append(l.indices, idx+int32(i), idx+int32((i+1)%nsegs))
	}
}

func (l *LinesDrawBuilder) AddLatLongCircle(p math.Point2LL, nmPerLongitude float32, r float32, nsegs int) {
	// We want vertices in lat-long space but will draw the circle in
	// nm space since distance is uniform there.
	pc := math.LL2NM(p, nmPerLongitude)
	for i := 0; i < nsegs; i++ {
		pt := func(i int) [2]float32 {
			a := float32(i) / float32(nsegs) * 2 * gomath.Pi
			v := [2]float32{math.Sin(a), math.Cos(a)}
			v = math.Scale2f(v, r)
			return math.NM2LL(math.Add2f(pc, v), nmPerLongitude)
		}
		l.AddLine(pt(i), pt(i+1))
	}
}

// Draws a number using digits drawn with lines. This can be helpful in
// cases like drawing an altitude on a video map where we want the number
// size to change when the user zooms the scope.
func (l *LinesDrawBuilder) AddNumber(p [2]float32, sz float32, v string) {
	// digit -> slice of line segments
	coords := [][][2][2]float32{
		{{{0, 2}, {2, 2}}, {{2, 2}, {2, 0}}, {{2, 0}, {0, 0}}, {{0, 0}, {0, 2}}},
		{{{1, 2}, {1, 0}}, {{1, 2}, {0.5, 1.5}}},
		{{{0, 2}, {2, 2}}, {{2, 2}, {2, 1}}, {{2, 1}, {0, 1}}, {{0, 1}, {0, 0}}, {{0, 0}, {2, 0}}},
		{{{0, 2}, {2, 2}}, {{2, 2}, {2, 0}}, {{2, 0}, {0, 0}}, {{1, 1}, {2, 1}}},
		{{{0, 1}, {2, 1}}, {{2, 2}, {2, 0}}, {{0, 2}, {0, 1}}},
		{{{2, 2}, {0, 2}}, {{0, 2}, {0, 1}}, {{0, 1}, {2, 1}}, {{2, 1}, {2, 0}}, {{2, 0}, {0, 0}}},
		{{{0, 0}, {2, 0}}, {{2, 0}, {2, 1}}, {{2, 1}, {0, 1}}, {{0, 0}, {0, 2}}, {{0, 2}, {1, 2}}},
		{{{0, 2}, {2, 2}}, {{2, 2}, {1, 0}}},
		{{{0, 2}, {2, 2}}, {{2, 2}, {2, 1}}, {{2, 1}, {0, 1}}, {{0, 1}, {0, 2}}, {{0, 1}, {2, 1}}, {{2, 1}, {2, 0}}, {{2, 0}, {0, 0}}, {{0, 0}, {0, 1}}},
		{{{1, 0}, {2, 0}}, {{2, 0}, {2, 2}}, {{2, 2}, {0, 2}}, {{0, 2}, {0, 1}}, {{0, 1}, {2, 1}}},
	}

	for _, digit := range v {
		d := digit - '0'
		if d >= 0 && d <= 9 {
			for _, seg := range coords[d] {
				l.AddLine(math.Add2f(p, math.Scale2f(seg[0], sz)), math.Add2f(p, math.Scale2f(seg[1], sz)))
			}
		} else {
			// draw an x
			l.AddLine(p, math.Add2f(p, math.Scale2f([2]float32{2, 2}, sz)))
			l.AddLine(math.Add2f(p, [2]float32{2 * sz, 0}), math.Add2f(p, [2]float32{0, 2 * sz}))
		}
		p[0] += 2.5 * sz
	}
}

// Bounds returns the 2D bounding box of the specified lines.
func (l *LinesDrawBuilder) Bounds() math.Extent2D {
	return math.Extent2DFromPoints(l.p)
}

// GenerateCommands adds commands to the specified command buffer to draw
// the lines stored in the LinesDrawBuilder.
func (l *LinesDrawBuilder) GenerateCommands(cb *CommandBuffer) {
	if len(l.indices) == 0 {
		return
	}

	// Add the vertex positions to the command buffer.
	p := cb.Float2Buffer(l.p)
	cb.VertexArray(p, 2, 2*4)

	// Add the vertex indices and issue the draw command.
	ind := cb.IntBuffer(l.indices)
	cb.DrawLines(ind, len(l.indices))

	// Clean up
	cb.DisableVertexArray()
}

// LinesDrawBuilders are managed using a sync.Pool so that their buf slice
// allocations persist across multiple uses.
var linesDrawBuilderPool = sync.Pool{New: func() any { return &LinesDrawBuilder{} }}

func GetLinesDrawBuilder() *LinesDrawBuilder {
	return linesDrawBuilderPool.Get().(*LinesDrawBuilder)
}

func ReturnLinesDrawBuilder(ld *LinesDrawBuilder) {
	ld.Reset()
	linesDrawBuilderPool.Put(ld)
}

// ColoredLinesDrawBuilder is similar to the LinesDrawBuilder though it
// allows specifying the color of each line individually.  Its methods
// otherwise mostly parallel those of LinesDrawBuilder; see the
// documentation there.
type ColoredLinesDrawBuilder struct {
	LinesDrawBuilder
	color []RGB
}

func (l *ColoredLinesDrawBuilder) Reset() {
	l.LinesDrawBuilder.Reset()
	l.color = l.color[:0]
}

func (l *ColoredLinesDrawBuilder) AddLine(p0, p1 [2]float32, color RGB) {
	l.LinesDrawBuilder.AddLine(p0, p1)
	l.color = append(l.color, color, color)
}

func (l *ColoredLinesDrawBuilder) AddLineLoop(color RGB, p [][2]float32) {
	l.LinesDrawBuilder.AddLineLoop(p)
	for range p {
		l.color = append(l.color, color)
	}
}

// AddCircle adds lines that draw the outline of a circle with specified
// radius and color centered at the specified point p. The nsegs parameter
// specifies the tessellation rate for the circle.
func (l *ColoredLinesDrawBuilder) AddCircle(p [2]float32, radius float32, nsegs int, color RGB) {
	l.LinesDrawBuilder.AddCircle(p, radius, nsegs)

	for i := 0; i < nsegs; i++ {
		l.color = append(l.color, color)
	}
}

func (l *ColoredLinesDrawBuilder) GenerateCommands(cb *CommandBuffer) (int, int) {
	if len(l.indices) == 0 {
		return 0, 0
	}

	rgb := cb.RGBBuffer(l.color)
	cb.RGB32Array(rgb, 3, 3*4)

	l.LinesDrawBuilder.GenerateCommands(cb)

	return rgb, 3 * len(l.color)
}

// ColoredLinesDrawBuilders are managed using a sync.Pool so that their buf
// slice allocations persist across multiple uses.
var coloredLinesDrawBuilderPool = sync.Pool{New: func() any { return &ColoredLinesDrawBuilder{} }}

func GetColoredLinesDrawBuilder() *ColoredLinesDrawBuilder {
	return coloredLinesDrawBuilderPool.Get().(*ColoredLinesDrawBuilder)
}

func ReturnColoredLinesDrawBuilder(ld *ColoredLinesDrawBuilder) {
	ld.Reset()
	coloredLinesDrawBuilderPool.Put(ld)
}

// TrianglesDrawBuilder collects triangles to be batched up in a single
// draw call. Note that it does not allow specifying per-vertex or
// per-triangle color; rather, the current color as specified by a call to
// the CommandBuffer SetRGB method is used for all triangles.
type TrianglesDrawBuilder struct {
	p       [][2]float32
	indices []int32
}

func (t *TrianglesDrawBuilder) Reset() {
	t.p = t.p[:0]
	t.indices = t.indices[:0]
}

// AddTriangle adds a triangle with the specified three vertices to be
// drawn.
func (t *TrianglesDrawBuilder) AddTriangle(p0, p1, p2 [2]float32) {
	idx := int32(len(t.p))
	t.p = append(t.p, p0, p1, p2)
	t.indices = append(t.indices, idx, idx+1, idx+2)
}

// AddQuad adds a quadrilateral with the specified four vertices to be
// drawn; the quad is split into two triangles for drawing.
func (t *TrianglesDrawBuilder) AddQuad(p0, p1, p2, p3 [2]float32) {
	idx := int32(len(t.p))
	t.p = append(t.p, p0, p1, p2, p3)
	t.indices = append(t.indices, idx, idx+1, idx+2, idx, idx+2, idx+3)
}

// AddCircle adds a filled circle with specified radius around the
// specified position to be drawn using triangles. The specified number of
// segments, nsegs, sets the tessellation rate for the circle.
func (t *TrianglesDrawBuilder) AddCircle(p [2]float32, radius float32, nsegs int) {
	circle := math.CirclePoints(nsegs)

	idx := int32(len(t.p))
	t.p = append(t.p, p) // center point
	for i := 0; i < nsegs; i++ {
		pi := [2]float32{p[0] + radius*circle[i][0], p[1] + radius*circle[i][1]}
		t.p = append(t.p, pi)
	}
	for i := 0; i < nsegs; i++ {
		t.indices = append(t.indices, idx, idx+1+int32(i), idx+1+int32((i+1)%nsegs))
	}
}

func (t *TrianglesDrawBuilder) AddLatLongCircle(p [2]float32, nmPerLongitude float32, r float32, nsegs int) {
	// Like LinesDrawBuilder AddLatLongCircle, do the work in nm space
	pc := math.LL2NM(p, nmPerLongitude)
	for i := 0; i < nsegs; i++ {
		pt := func(i int) [2]float32 {
			a := float32(i) / float32(nsegs) * 2 * gomath.Pi
			v := [2]float32{math.Sin(a), math.Cos(a)}
			v = math.Scale2f(v, r)
			return math.NM2LL(math.Add2f(pc, v), nmPerLongitude)
		}
		t.AddTriangle(p, pt(i), pt(i+1))
	}
}

func (t *TrianglesDrawBuilder) Bounds() math.Extent2D {
	return math.Extent2DFromPoints(t.p)
}

func (t *TrianglesDrawBuilder) GenerateCommands(cb *CommandBuffer) {
	if len(t.indices) == 0 {
		return
	}

	p := cb.Float2Buffer(t.p)
	cb.VertexArray(p, 2, 2*4)

	ind := cb.IntBuffer(t.indices)
	cb.DrawTriangles(ind, len(t.indices))

	cb.DisableVertexArray()
}

// TrianglesDrawBuilders are managed using a sync.Pool so that their buf
// slice allocations persist across multiple uses.
var trianglesDrawBuilderPool = sync.Pool{New: func() any { return &TrianglesDrawBuilder{} }}

func GetTrianglesDrawBuilder() *TrianglesDrawBuilder {
	return trianglesDrawBuilderPool.Get().(*TrianglesDrawBuilder)
}

func ReturnTrianglesDrawBuilder(td *TrianglesDrawBuilder) {
	td.Reset()
	trianglesDrawBuilderPool.Put(td)
}

// ColoredTrianglesDrawBuilder
type ColoredTrianglesDrawBuilder struct {
	TrianglesDrawBuilder
	color []RGB
}

func (t *ColoredTrianglesDrawBuilder) Reset() {
	t.TrianglesDrawBuilder.Reset()
	t.color = t.color[:0]
}

// AddTriangle adds a triangle with the specified three vertices to be
// drawn.
func (t *ColoredTrianglesDrawBuilder) AddTriangle(p0, p1, p2 [2]float32, rgb RGB) {
	t.TrianglesDrawBuilder.AddTriangle(p0, p1, p2)
	t.color = append(t.color, rgb, rgb, rgb)
}

// AddQuad adds a quadrilateral with the specified four vertices to be
// drawn; the quad is split into two triangles for drawing.
func (t *ColoredTrianglesDrawBuilder) AddQuad(p0, p1, p2, p3 [2]float32, rgb RGB) {
	t.TrianglesDrawBuilder.AddQuad(p0, p1, p2, p3)
	t.color = append(t.color, rgb, rgb, rgb, rgb)
}

// AddCircle adds a filled circle with specified radius around the
// specified position to be drawn using triangles. The specified number of
// segments, nsegs, sets the tessellation rate for the circle.
func (t *ColoredTrianglesDrawBuilder) AddCircle(p [2]float32, radius float32, nsegs int, rgb RGB) {
	t.TrianglesDrawBuilder.AddCircle(p, radius, nsegs)
	for i := 0; i < nsegs; i++ {
		t.color = append(t.color, rgb)
	}
}

func (t *ColoredTrianglesDrawBuilder) GenerateCommands(cb *CommandBuffer) {
	if len(t.indices) == 0 {
		return
	}

	rgb := cb.RGBBuffer(t.color)
	cb.RGB32Array(rgb, 3, 3*4)

	t.TrianglesDrawBuilder.GenerateCommands(cb)

	cb.DisableColorArray()
}

// ColoredTrianglesDrawBuilders are managed using a sync.Pool so that their buf
// slice allocations persist across multiple uses.
var coloredTrianglesDrawBuilderPool = sync.Pool{New: func() any { return &ColoredTrianglesDrawBuilder{} }}

func GetColoredTrianglesDrawBuilder() *ColoredTrianglesDrawBuilder {
	return coloredTrianglesDrawBuilderPool.Get().(*ColoredTrianglesDrawBuilder)
}

func ReturnColoredTrianglesDrawBuilder(td *ColoredTrianglesDrawBuilder) {
	td.Reset()
	coloredTrianglesDrawBuilderPool.Put(td)
}

// TexturedTrianglesDrawBuilder generates commands for drawing a set of
// triangles with associated uv texture coordinates using a specified
// single texture map.
type TexturedTrianglesDrawBuilder struct {
	TrianglesDrawBuilder
	uv [][2]float32
}

func (t *TexturedTrianglesDrawBuilder) Reset() {
	t.TrianglesDrawBuilder.Reset()
	t.uv = t.uv[:0]
}

// AddTriangle adds a triangle with the specified three vertices and uv
// coordinates to be drawn.
func (t *TexturedTrianglesDrawBuilder) AddTriangle(p0, p1, p2 [2]float32, uv0, uv1, uv2 [2]float32) {
	t.TrianglesDrawBuilder.AddTriangle(p0, p1, p2)
	t.uv = append(t.uv, uv0, uv1, uv2)
}

// AddQuad adds a quadrilateral with the specified four vertices and
// associated texture coordinates to the list to be drawn; the quad is
// split into two triangles for drawing.
func (t *TexturedTrianglesDrawBuilder) AddQuad(p0, p1, p2, p3 [2]float32, uv0, uv1, uv2, uv3 [2]float32) {
	t.TrianglesDrawBuilder.AddQuad(p0, p1, p2, p3)
	t.uv = append(t.uv, uv0, uv1, uv2, uv3)
}

func (t *TexturedTrianglesDrawBuilder) GenerateCommands(cb *CommandBuffer) {
	if len(t.indices) == 0 {
		return
	}

	uv := cb.Float2Buffer(t.uv)
	cb.TexCoordArray(uv, 2, 2*4)

	t.TrianglesDrawBuilder.GenerateCommands(cb)

	cb.DisableTexCoordArray()
}

// And as above, these are also managed in a pool.
var texturedTrianglesDrawBuilderPool = sync.Pool{New: func() any { return &TexturedTrianglesDrawBuilder{} }}

func GetTexturedTrianglesDrawBuilder() *TexturedTrianglesDrawBuilder {
	return texturedTrianglesDrawBuilderPool.Get().(*TexturedTrianglesDrawBuilder)
}

func ReturnTexturedTrianglesDrawBuilder(td *TexturedTrianglesDrawBuilder) {
	td.Reset()
	texturedTrianglesDrawBuilderPool.Put(td)
}

// TextDrawBuilder accumulates text to be drawn, batching it up in a single
// draw command.
type TextDrawBuilder struct {
	// Vertex/index buffers for regular text and drop shadows, if enabled.
	regular map[uint32]*TextBuffers // Map from texid to buffers

	// Buffers for background quads, if specified (shared for all tex ids)
	background struct {
		p       [][2]float32
		rgb     []RGB
		indices []int32
	}
}

// TextBuffers is a helper class that maintains vertex and index buffers
// for drawing text.
type TextBuffers struct {
	p       [][2]float32
	uv      [][2]float32
	rgb     []RGB
	indices []int32
}

func (t *TextBuffers) Reset() {
	t.p = t.p[:0]
	t.uv = t.uv[:0]
	t.rgb = t.rgb[:0]
	t.indices = t.indices[:0]
}

// Add updates the buffers to draw the given glyph with the given color,
// with upper-left coordinates specified by p.
func (t *TextBuffers) Add(p [2]float32, glyph *Glyph, color RGB) {
	// Get the vertex positions and texture coordinates for the
	// glyph.
	u0, v0, u1, v1 := glyph.U0, glyph.V0, glyph.U1, glyph.V1
	x0, y0, x1, y1 := glyph.X0, glyph.Y0, glyph.X1, glyph.Y1

	// Add the quad for the glyph to the vertex/index buffers
	startIdx := int32(len(t.p))
	t.uv = append(t.uv, [][2]float32{{u0, v0}, {u1, v0}, {u1, v1}, {u0, v1}}...)
	t.rgb = append(t.rgb, color, color, color, color)
	t.p = append(t.p, [][2]float32{
		math.Add2f(p, [2]float32{x0, -y0}),
		math.Add2f(p, [2]float32{x1, -y0}),
		math.Add2f(p, [2]float32{x1, -y1}),
		math.Add2f(p, [2]float32{x0, -y1})}...)
	t.indices = append(t.indices, startIdx, startIdx+1, startIdx+2, startIdx+3)
}

func (t *TextBuffers) GenerateCommands(cb *CommandBuffer) {
	if len(t.indices) == 0 {
		return
	}

	// Enable the assorted vertex buffers.
	p := cb.Float2Buffer(t.p)
	cb.VertexArray(p, 2, 2*4)
	rgb := cb.RGBBuffer(t.rgb)
	cb.RGB32Array(rgb, 3, 3*4)
	uv := cb.Float2Buffer(t.uv)
	cb.TexCoordArray(uv, 2, 2*4)

	// Enable the index buffer and issue the draw command.
	ind := cb.IntBuffer(t.indices)
	cb.DrawQuads(ind, len(t.indices))
}

// TextStyle specifies the style of text to be drawn.
type TextStyle struct {
	Font  *Font
	Color RGB
	// LineSpacing gives the additional spacing in pixels between lines of
	// text relative to the font's default line spacing.
	LineSpacing int
	// DrawBackground specifies if a filled quadrilateral should be drawn behind
	// the text (e.g., to offset it to make it more legible.)
	DrawBackground bool
	// BackgroundColor specifies the color of the background; it is only used if
	// DrawBackground is grue.
	BackgroundColor RGB
}

// AddTextCentered draws the specified text centered at the specified
// position p.
func (td *TextDrawBuilder) AddTextCentered(text string, p [2]float32, style TextStyle) {
	bx, by := style.Font.BoundText(text, 0)
	p[0] -= float32(bx) / 2
	p[1] += float32(by) / 2
	td.AddText(text, p, style)
}

// AddText draws the specified text using the given position p as the
// upper-left corner.
func (td *TextDrawBuilder) AddText(s string, p [2]float32, style TextStyle) [2]float32 {
	return td.AddTextMulti([]string{s}, p, []TextStyle{style})
}

// AddTextMulti draws multiple blocks of text with multiple styles, with
// the first block of text starting at the specified point p.  Subsequent
// blocks begin immediately after the end of the previous block.
func (td *TextDrawBuilder) AddTextMulti(text []string, p [2]float32, styles []TextStyle) [2]float32 {
	// Current cursor position
	px, py := p[0], p[1]

	for i := range text {
		style := styles[i]

		// Total between subsequent lines, vertically.
		dy := float32(style.Font.Size + style.LineSpacing)

		// Bounds for the current line's background box, if needed
		bx0, by0 := px, py

		// Utility function that is called at the end of each line when
		// DrawBackground is enabled; this takes the accumulated bounding
		// box of the current line and emits a background quad for it.
		flushbg := func() {
			// Second corner of the bounding box
			bx1, by1 := px, py-dy

			// Add a quad to stake out the background for this line.
			startIdx := int32(len(td.background.p))
			color := style.BackgroundColor
			td.background.rgb = append(td.background.rgb, color, color, color, color)
			// Additional padding
			padx, pady := float32(1), float32(0)
			// Emit the four vertices of the line's bound, padded.
			td.background.p = append(td.background.p, [][2]float32{
				{bx0 - padx, by0 - pady},
				{bx1 + padx, by0 - pady},
				{bx1 + padx, by1 + pady},
				{bx0 - padx, by1 + pady}}...)
			td.background.indices = append(td.background.indices, startIdx, startIdx+1, startIdx+2, startIdx+3)
		}

		for _, ch := range text[i] {
			glyph := style.Font.LookupGlyph(ch)

			if ch == '\n' {
				// End of line handling. First emit the background quad, if
				// selected.
				if style.DrawBackground {
					flushbg()
				}

				// Update the cursor to go to the next line.
				px = p[0]
				py -= dy

				// Reset the upper line box corner for the start of the
				// next line.
				bx0, by0 = px, py

				// And skip over the drawing code for the newline...
				continue
			}

			// Don't do any drawing if the glyph is marked as invisible;
			// beyond the small perf. cost, we'll end up getting "?" and
			// the like if we do this anyway.
			if glyph.Visible {
				if td.regular == nil {
					td.regular = make(map[uint32]*TextBuffers)
				}
				if _, ok := td.regular[style.Font.TexId]; !ok {
					td.regular[style.Font.TexId] = &TextBuffers{}
				}
				td.regular[style.Font.TexId].Add([2]float32{px, py}, glyph, style.Color)
			}

			// Visible or not, advance the x cursor position to move to the next character.
			px += glyph.AdvanceX
		}

		// Make sure we emit a background quad for the last line even if it
		// doesn't end with a newline.
		if style.DrawBackground {
			flushbg()
		}
	}
	return [2]float32{px, py}
}

func (td *TextDrawBuilder) Reset() {
	for _, regular := range td.regular {
		regular.Reset()
	}

	td.background.p = td.background.p[:0]
	td.background.rgb = td.background.rgb[:0]
	td.background.indices = td.background.indices[:0]
}

func (td *TextDrawBuilder) GenerateCommands(cb *CommandBuffer) {
	// Issue the commands to draw the background first, if any background
	// quads have been specified.
	if len(td.background.indices) > 0 {
		p := cb.Float2Buffer(td.background.p)
		cb.VertexArray(p, 2, 2*4)

		rgb := cb.RGBBuffer(td.background.rgb)
		cb.RGB32Array(rgb, 3, 3*4)

		ind := cb.IntBuffer(td.background.indices)
		cb.DrawQuads(ind, len(td.background.indices))
	}

	// Enable blending so that we get antialiasing at character edges
	// (which have fractional alpha in the atlas texture.)
	cb.Blend()

	// Draw them in order of texture id (arbitrary, but consistent across
	// frames.)  Note that this doesn't necessarily precisely follow the
	// draw order from the user, so drawing from two atlases where
	// characters from different atlases overlap may not turn out as
	// expected. We'll assume that's not worth worrying about...
	for _, id := range util.SortedMapKeys(td.regular) {
		regular := td.regular[id]
		if len(regular.indices) == 0 {
			continue
		}

		// Enable the texture with the font atlas
		cb.EnableTexture(id)

		regular.GenerateCommands(cb)
	}

	// Clean up after ourselves.
	cb.DisableVertexArray()
	cb.DisableColorArray()
	cb.DisableTexCoordArray()
	cb.DisableTexture()
	cb.DisableBlend()
}

// TextDrawBuilders are managed using a sync.Pool so that their buf slice
// allocations persist across multiple uses.
var textDrawBuilderPool = sync.Pool{New: func() any { return &TextDrawBuilder{} }}

func GetTextDrawBuilder() *TextDrawBuilder {
	return textDrawBuilderPool.Get().(*TextDrawBuilder)
}

func ReturnTextDrawBuilder(td *TextDrawBuilder) {
	td.Reset()
	textDrawBuilderPool.Put(td)
}

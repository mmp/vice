// renderer.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"io"
	"math"
	"unsafe"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/mmp/imgui-go/v4"
)

// Renderer defines an interface for all of the various drawing that happens in vice.
// There is currently a single implementation of it--OpenGL2Renderer--though having
// all of these details behind the Renderer interface would make it realtively easy
// to write a Vulkan, Metal, or DirectX rendering backend.
type Renderer interface {
	// RenderImgui translates the ImGui draw data to OpenGL commands.
	RenderImgui(displaySize [2]float32, framebufferSize [2]float32, drawData imgui.DrawData)

	// CreateTextureFromPNG returns an identifier for a texture map defined
	// by a PNG file from the provided Reader.
	CreateTextureFromPNG(r io.Reader) (uint32, error)

	// RenderCommandBuffer executes all of the commands encoded in the
	// provided command buffer, returning statistics about what was
	// rendered.
	RenderCommandBuffer(*CommandBuffer) RendererStats

	// Dispose releases resources allocated by the renderer.
	Dispose()
}

// RendererStats encapsulates assorted statistics from rendering.
type RendererStats struct {
	nBuffers, bufferBytes               int
	nDrawCalls                          int
	nPoints, nLines, nTriangles, nQuads int
}

func (rs *RendererStats) String() string {
	return fmt.Sprintf("%d buffers (%.2f MB), %d draw calls: %d points, %d lines, %d tris, %d quads",
		rs.nBuffers, float32(rs.bufferBytes)/(1024*1024), rs.nDrawCalls, rs.nPoints, rs.nLines, rs.nTriangles, rs.nQuads)
}

func (rs *RendererStats) Merge(s RendererStats) {
	rs.nBuffers += s.nBuffers
	rs.bufferBytes += s.bufferBytes
	rs.nDrawCalls += s.nDrawCalls
	rs.nPoints += s.nPoints
	rs.nLines += s.nLines
	rs.nTriangles += s.nTriangles
	rs.nQuads += s.nQuads
}

///////////////////////////////////////////////////////////////////////////

// The command buffer stores a series of rendering commands, represented by
// the following values. Each one is followed in the buffer by a number of
// command arguments, after which the next command follows.  Comments
// after each command briefly describe its arguments.
//
// Buffers (vertex, index, color, texcoord), are all stored directly in the
// CommandBuffer, following RendererFloatBuffer and RendererIntBuffer
// commands; the first argument after those commands is the length of the
// buffer and then its values follow directly. Rendering commands that use
// buffers (e.g., buffer binding commands like RendererVertexArray or draw
// commands like RendererDrawLines) are then directed to those buffers via
// integer parameters that encode the offset from the start of the command
// buffer where a buffer begins. (Note that this implies that one
// CommandBuffer cannot refer to a vertex/index buffer in another
// CommandBuffer.

const (
	RendererLoadProjectionMatrix = iota // 16 float32: matrix
	RendererLoadModelViewMatrix         // 16 float32: matrix
	RendererClearRGBA                   // 4 float32: RGBA
	RendererScissor                     // 4 int32: x, y, width, height
	RendererViewport                    // 4 int32: x, y, width, height
	RendererBlend                       // no args: for now always src alpha, 1-src alpha
	RendererSetRGBA                     // 4 float32: RGBA
	RendererDisableBlend                // no args
	RendererFloatBuffer                 // int32 size, then size*float32 values
	RendererIntBuffer                   // int32: size, then size*int32 values
	RendererEnableTexture               // int32 handle
	RendererDisableTexture              // no args
	RendererVertexArray                 // int32 offset to array values, n components, stride (bytes)
	RendererDisableVertexArray          // no args
	RendererColorArray                  // int32 offset to array values, n components, stride (bytes)
	RendererDisableColorArray           // no args
	RendererTexCoordArray               // int32 offset to array values, n components, stride (bytes)
	RendererDisableTexCoordArray        // no args
	RendererPointSize                   // float32
	RendererDrawPoints                  // 2 int32: offset to the index buffer, count
	RendererLineWidth                   // float32
	RendererDrawLines                   // 2 int32: offset to the index buffer, count
	RendererDrawTriangles               // 2 int32: offset to the index buffer, count
	RendererDrawQuads                   // 2 int32: offset to the index buffer, count
	RendererCallBuffer                  // 2 int32: buffer ptr (low bits then high bits), length
	RendererResetState                  // no args
)

// CommandBuffer encodes a sequence of rendering commands in an
// API-agnostic manner. It makes it possible for other parts of vice to
// "pre-bake" rendering work into a form that can be efficiently processed
// by a Renderer and possibly reused over multiple frames.
type CommandBuffer struct {
	buf []uint32
}

// Reset resets the command buffer's length to zero so that it can be
// reused.
func (cb *CommandBuffer) Reset() {
	cb.buf = cb.buf[:0]
}

// growFor ensures that at least n more values can be added to the end of
// the buffer without going past its capacity.
func (cb *CommandBuffer) growFor(n int) {
	if len(cb.buf)+n > cap(cb.buf) {
		sz := 2 * cap(cb.buf)
		if sz < 1024 {
			sz = 1024
		}
		if sz < len(cb.buf)+n {
			sz = 2 * (len(cb.buf) + n)
		}
		b := make([]uint32, len(cb.buf), sz)
		copy(b, cb.buf)
		cb.buf = b
	}
}

func (cb *CommandBuffer) appendFloats(floats ...float32) {
	for _, f := range floats {
		// Convert each one to a uint32 since that's the type that is
		// actually stored...
		cb.buf = append(cb.buf, math.Float32bits(f))
	}
}

func (cb *CommandBuffer) appendInts(ints ...int) {
	for _, i := range ints {
		if i != int(uint32(i)) {
			lg.Errorf("%d: attempting to add non-32-bit value to CommandBuffer", i)
		}
		cb.buf = append(cb.buf, uint32(i))
	}
}

// FloatSlice returns a []float32 for the specified segment of the command
// buffer.  It is up to the caller to be sure that this region actually
// stores float32 values.  This method allows code to patch data in an
// already-generated CommandBuffer, for example to change colors in a color
// buffer without needing to regenerate a new command buffer from scratch.
func (cb *CommandBuffer) FloatSlice(start, length int) []float32 {
	if length == 0 {
		return nil
	}
	ptr := (*float32)(unsafe.Pointer(&cb.buf[start]))
	return unsafe.Slice(ptr, length)
}

func (cb *CommandBuffer) LoadProjectionMatrix(m mgl32.Mat4) {
	cb.appendInts(RendererLoadProjectionMatrix)
	for i := 0; i < 16; i++ {
		cb.appendFloats(m[i])
	}
}

func (cb *CommandBuffer) LoadModelViewMatrix(m mgl32.Mat4) {
	cb.appendInts(RendererLoadModelViewMatrix)
	for i := 0; i < 16; i++ {
		cb.appendFloats(m[i])
	}
}

func (cb *CommandBuffer) ClearRGB(color RGB) {
	cb.appendInts(RendererClearRGBA)
	cb.appendFloats(color.R, color.G, color.B, 1)
}

func (cb *CommandBuffer) Scissor(x, y, w, h int) {
	cb.appendInts(RendererScissor, x, y, w, h)
}

func (cb *CommandBuffer) Viewport(x, y, w, h int) {
	cb.appendInts(RendererViewport, x, y, w, h)
}

func (cb *CommandBuffer) SetRGBA(rgba RGBA) {
	cb.appendInts(RendererSetRGBA)
	cb.appendFloats(rgba.R, rgba.G, rgba.B, rgba.A)
}

func (cb *CommandBuffer) SetRGB(rgb RGB) {
	cb.appendInts(RendererSetRGBA)
	cb.appendFloats(rgb.R, rgb.G, rgb.B, 1)
}

func (cb *CommandBuffer) Blend() {
	cb.appendInts(RendererBlend)
}

func (cb *CommandBuffer) DisableBlend() {
	cb.appendInts(RendererDisableBlend)
}

// Float2Buffer stores the provided slice of [2]float32 values in the
// CommandBuffer and returns the offset where the first value of the slice
// is stored; this offset can then be passed to commands like VertexArray
// to specify this buffer.
func (cb *CommandBuffer) Float2Buffer(buf [][2]float32) int {
	cb.appendInts(RendererFloatBuffer, 2*len(buf))
	offset := len(cb.buf)

	n := 2 * len(buf)
	cb.growFor(n)
	start := len(cb.buf)
	cb.buf = cb.buf[:start+n]
	copy(cb.buf[start:start+n], unsafe.Slice((*uint32)(unsafe.Pointer(&buf[0])), n))

	return offset
}

// RGBBuffer stores the provided slices of RGB values in the command buffer
// and returns the offset where the first value of the slice is stored.
func (cb *CommandBuffer) RGBBuffer(buf []RGB) int {
	cb.appendInts(RendererFloatBuffer, 3*len(buf))
	offset := len(cb.buf)

	n := 3 * len(buf)
	cb.growFor(n)
	start := len(cb.buf)
	copy(cb.buf[start:start+n], unsafe.Slice((*uint32)(unsafe.Pointer(&buf[0])), n))
	cb.buf = cb.buf[:start+n]

	return offset
}

// IntBuffer stores the provided slices of RGB values in the command buffer
// and returns the offset where the first value of the slice is stored.
func (cb *CommandBuffer) IntBuffer(buf []int32) int {
	cb.appendInts(RendererIntBuffer, len(buf))
	offset := len(cb.buf)

	n := len(buf)
	cb.growFor(n)
	start := len(cb.buf)
	copy(cb.buf[start:start+n], unsafe.Slice((*uint32)(unsafe.Pointer(&buf[0])), n))
	cb.buf = cb.buf[:start+n]

	return offset
}

func (cb *CommandBuffer) EnableTexture(id uint32) {
	cb.appendInts(RendererEnableTexture, int(id))
}

func (cb *CommandBuffer) DisableTexture() {
	cb.appendInts(RendererDisableTexture)
}

func (cb *CommandBuffer) VertexArray(offset, nComps, stride int) {
	cb.appendInts(RendererVertexArray, offset, nComps, stride)
}

func (cb *CommandBuffer) DisableVertexArray() {
	cb.appendInts(RendererDisableVertexArray)
}

func (cb *CommandBuffer) ColorArray(offset, nComps, stride int) {
	cb.appendInts(RendererColorArray, offset, nComps, stride)
}

func (cb *CommandBuffer) DisableColorArray() {
	cb.appendInts(RendererDisableColorArray)
}

func (cb *CommandBuffer) TexCoordArray(offset, nComps, stride int) {
	cb.appendInts(RendererTexCoordArray, offset, nComps, stride)
}

func (cb *CommandBuffer) DisableTexCoordArray() {
	cb.appendInts(RendererDisableTexCoordArray)
}

func (cb *CommandBuffer) PointSize(w float32) {
	cb.appendInts(RendererPointSize)
	cb.appendFloats(w)
}

func (cb *CommandBuffer) DrawPoints(offset, count int) {
	cb.appendInts(RendererDrawPoints, offset, count)
}

func (cb *CommandBuffer) LineWidth(w float32) {
	cb.appendInts(RendererLineWidth)
	cb.appendFloats(w)
}

func (cb *CommandBuffer) DrawLines(offset, count int) {
	cb.appendInts(RendererDrawLines, offset, count)
}

func (cb *CommandBuffer) DrawTriangles(offset, count int) {
	cb.appendInts(RendererDrawTriangles, offset, count)
}

func (cb *CommandBuffer) DrawQuads(offset, count int) {
	cb.appendInts(RendererDrawQuads, offset, count)
}

func (cb *CommandBuffer) Call(sub CommandBuffer) {
	up := uintptr(unsafe.Pointer(&sub.buf[0]))
	cb.appendInts(RendererCallBuffer, int(up&0xffffffff), int(up>>32), len(sub.buf))
}

func (cb *CommandBuffer) ResetState() {
	cb.appendInts(RendererResetState)
}

// TODO: this feels out of place here
func (cb *CommandBuffer) UseWindowCoordinates(w, h float32) {
	proj := mgl32.Ortho2D(0, w, 0, h)
	cb.LoadProjectionMatrix(proj)
	cb.LoadModelViewMatrix(mgl32.Ident4())
}

///////////////////////////////////////////////////////////////////////////
// DrawBuilders

// The various *DrawBuilder classes provide capabilities for specifying a
// number of independent things of the same type to draw and then
// generating corresponding buffer storage and draw commands in a
// CommandBuffer.  This allows batching up many things to be drawn all in a
// single draw command, with corresponding GPU performance benefits.

type PointsDrawBuilder struct {
	p       [][2]float32
	color   []RGB
	indices []int32
}

func (p *PointsDrawBuilder) Reset() {
	p.p = p.p[:0]
	p.color = p.color[:0]
	p.indices = p.indices[:0]
}

func (p *PointsDrawBuilder) AddPoint(pt [2]float32, color RGB) {
	p.p = append(p.p, pt)
	p.color = append(p.color, color)
	p.indices = append(p.indices, int32(len(p.p)-1))
}

func (p *PointsDrawBuilder) Bounds() Extent2D {
	return Extent2DFromPoints(p.p)
}

func (p *PointsDrawBuilder) GenerateCommands(cb *CommandBuffer) {
	if len(p.indices) == 0 {
		return
	}

	pi := cb.Float2Buffer(p.p)
	cb.VertexArray(pi, 2, 2*4)

	if len(p.color) > 0 {
		rgb := cb.RGBBuffer(p.color)
		cb.ColorArray(rgb, 3, 3*4)
	}

	ind := cb.IntBuffer(p.indices)
	cb.DrawPoints(ind, len(p.indices))
}

type LinesDrawBuilder struct {
	p       [][2]float32
	indices []int32
}

func (l *LinesDrawBuilder) Reset() {
	l.p = l.p[:0]
	l.indices = l.indices[:0]
}

func (l *LinesDrawBuilder) AddLine(p0, p1 [2]float32) {
	idx := int32(len(l.p))
	l.p = append(l.p, p0, p1)
	l.indices = append(l.indices, idx, idx+1)
}

func (l *LinesDrawBuilder) AddPolyline(p [2]float32, shape [][2]float32) {
	idx := int32(len(l.p))
	for _, delta := range shape {
		pp := add2ll(p, delta)
		l.p = append(l.p, pp)
	}
	for i := 0; i < len(shape); i++ {
		l.indices = append(l.indices, idx+int32(i), idx+int32((i+1)%len(shape)))
	}
}

func (l *LinesDrawBuilder) Bounds() Extent2D {
	return Extent2DFromPoints(l.p)
}

func (l *LinesDrawBuilder) GenerateCommands(cb *CommandBuffer) {
	if len(l.indices) == 0 {
		return
	}

	p := cb.Float2Buffer(l.p)
	cb.VertexArray(p, 2, 2*4)

	ind := cb.IntBuffer(l.indices)
	cb.DrawLines(ind, len(l.indices))
}

type ColoredLinesDrawBuilder struct {
	p       [][2]float32
	color   []RGB
	indices []int32
}

func (l *ColoredLinesDrawBuilder) Reset() {
	l.p = l.p[:0]
	l.color = l.color[:0]
	l.indices = l.indices[:0]
}

func (l *ColoredLinesDrawBuilder) AddLine(p0, p1 [2]float32, color RGB) {
	idx := int32(len(l.p))
	l.p = append(l.p, p0, p1)
	l.color = append(l.color, color, color)
	l.indices = append(l.indices, idx, idx+1)
}

func (l *ColoredLinesDrawBuilder) AddPolyline(p [2]float32, color RGB, shape [][2]float32) {
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

func (l *ColoredLinesDrawBuilder) Bounds() Extent2D {
	return Extent2DFromPoints(l.p)
}

func (l *ColoredLinesDrawBuilder) GenerateCommands(cb *CommandBuffer) (int, int) {
	if len(l.indices) == 0 {
		return 0, 0
	}

	p := cb.Float2Buffer(l.p)
	cb.VertexArray(p, 2, 2*4)

	rgb := cb.RGBBuffer(l.color)
	cb.ColorArray(rgb, 3, 3*4)

	ind := cb.IntBuffer(l.indices)
	cb.DrawLines(ind, len(l.indices))

	return rgb, 3 * len(l.color)
}

type TrianglesDrawBuilder struct {
	p       [][2]float32
	indices []int32
}

func (t *TrianglesDrawBuilder) Reset() {
	t.p = t.p[:0]
	t.indices = t.indices[:0]
}

func (t *TrianglesDrawBuilder) AddTriangle(p0, p1, p2 [2]float32) {
	idx := int32(len(t.p))
	t.p = append(t.p, p0, p1, p2)
	t.indices = append(t.indices, idx, idx+1, idx+2)
}

func (t *TrianglesDrawBuilder) Bounds() Extent2D {
	return Extent2DFromPoints(t.p)
}

func (t *TrianglesDrawBuilder) GenerateCommands(cb *CommandBuffer) {
	if len(t.indices) == 0 {
		return
	}

	p := cb.Float2Buffer(t.p)
	cb.VertexArray(p, 2, 2*4)

	ind := cb.IntBuffer(t.indices)
	cb.DrawTriangles(ind, len(t.indices))
}

type TextDrawBuilder struct {
	p       [][2]float32 // window coordinates
	uv      [][2]float32
	rgb     []RGB
	indices []int32

	// for background quads
	bgp       [][2]float32
	bgrgb     []RGB
	bgindices []int32
}

type TextStyle struct {
	font            *Font
	color           RGB
	lineSpacing     int
	drawBackground  bool
	backgroundColor RGB
}

func (td *TextDrawBuilder) AddTextCentered(text string, p [2]float32, style TextStyle) {
	bx, by := style.font.BoundText(text, 0)
	p[0] -= float32(bx) / 2
	p[1] += float32(by) / 2
	td.AddText(text, p, style)
}

// p is in pane coordinates: (0,0) is lower left corner
func (td *TextDrawBuilder) AddText(s string, p [2]float32, style TextStyle) {
	td.AddTextMulti([]string{s}, p, []TextStyle{style})
}

func (td *TextDrawBuilder) AddTextMulti(text []string, p [2]float32, styles []TextStyle) {
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

func (td *TextDrawBuilder) Reset() {
	td.p = td.p[:0]
	td.uv = td.uv[:0]
	td.rgb = td.rgb[:0]
	td.indices = td.indices[:0]
	td.bgp = td.bgp[:0]
	td.bgrgb = td.bgrgb[:0]
	td.bgindices = td.bgindices[:0]
}

func (td *TextDrawBuilder) GenerateCommands(cb *CommandBuffer) {
	if len(td.bgindices) > 0 {
		p := cb.Float2Buffer(td.bgp)
		cb.VertexArray(p, 2, 2*4)

		if len(td.bgrgb) > 0 {
			rgb := cb.RGBBuffer(td.bgrgb)
			cb.ColorArray(rgb, 3, 3*4)
		}

		ind := cb.IntBuffer(td.bgindices)
		cb.DrawQuads(ind, len(td.bgindices))
	}

	if len(td.indices) > 0 {
		texid := uint32(imgui.CurrentIO().Fonts().GetTextureID())
		cb.Blend()
		cb.EnableTexture(texid)

		p := cb.Float2Buffer(td.p)
		cb.VertexArray(p, 2, 2*4)

		if len(td.rgb) > 0 {
			rgb := cb.RGBBuffer(td.rgb)
			cb.ColorArray(rgb, 3, 3*4)
		}
		if len(td.uv) > 0 {
			uv := cb.Float2Buffer(td.uv)
			cb.TexCoordArray(uv, 2, 2*4)
		}

		ind := cb.IntBuffer(td.indices)
		cb.DrawQuads(ind, len(td.indices))

		cb.DisableTexture()
		cb.DisableBlend()
	}
}

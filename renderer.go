// renderer.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"image"
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

	// CreateTextureFromImage returns an identifier for a texture map defined
	// by the specified image.
	CreateTextureFromImage(image image.Image, generateMIPs bool) (id uint32, err error)

	UpdateTextureFromImage(id uint32, image image.Image, generateMIPs bool) error

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

// ClearRGB adds a command to the command buffer to clear the framebuffer
// to the specified RGB color.
func (cb *CommandBuffer) ClearRGB(color RGB) {
	cb.appendInts(RendererClearRGBA)
	cb.appendFloats(color.R, color.G, color.B, 1)
}

// Scissor adds a command to the command buffer to set the scissor
// rectangle as specified.
func (cb *CommandBuffer) Scissor(x, y, w, h int) {
	cb.appendInts(RendererScissor, x, y, w, h)
}

// Viewport adds a command to the command buffer to set the viewport to the
// specified rectangle.
func (cb *CommandBuffer) Viewport(x, y, w, h int) {
	cb.appendInts(RendererViewport, x, y, w, h)
}

// SetRGBA adds a command to the command buffer to set the current RGBA
// color. Subsequent draw commands will inherit this color unless they
// specify e.g., per-vertex colors themselves.
func (cb *CommandBuffer) SetRGBA(rgba RGBA) {
	cb.appendInts(RendererSetRGBA)
	cb.appendFloats(rgba.R, rgba.G, rgba.B, rgba.A)
}

// SetRGBA adds a command to the command buffer to set the current RGB
// color (alpha is set to 1). Subsequent draw commands will inherit this
// color unless they specify e.g., per-vertex colors themselves.
func (cb *CommandBuffer) SetRGB(rgb RGB) {
	cb.appendInts(RendererSetRGBA)
	cb.appendFloats(rgb.R, rgb.G, rgb.B, 1)
}

// Blend adds a command to the command buffer enable blending.  The blend
// mode cannot be specified currently, since only one mode (alpha over
// blending) is used.
func (cb *CommandBuffer) Blend() {
	cb.appendInts(RendererBlend)
}

// DisableBlend adds a command to the command buffer that disables
// blending.
func (cb *CommandBuffer) DisableBlend() {
	cb.appendInts(RendererDisableBlend)
}

// Float2Buffer stores the provided slice of [2]float32 values in the
// CommandBuffer and returns the offset where the first value of the slice
// is stored; this offset can then be passed to commands like VertexArray
// to specify this array.
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

// EnableTexture enables texturing from the specified texture id (as
// returned by the Renderer CreateTextureFromImage method implementation).
func (cb *CommandBuffer) EnableTexture(id uint32) {
	cb.appendInts(RendererEnableTexture, int(id))
}

// DisableTexture adds a command to the command buffer to disable
// texturing.
func (cb *CommandBuffer) DisableTexture() {
	cb.appendInts(RendererDisableTexture)
}

// VertexArray adds a command to the command buffer that specifies an array
// of vertex coordinates to use for a subsequent draw command. offset gives
// the offset into the current command buffer where the vertices again (e.g.,
// as returned by Float2Buffer), nComps is the number of components per
// vertex (generally 2 for vice), and stride gives the stride in bytes
// between vertices (e.g., 8 for densely packed 2D vertex coordinates.)
func (cb *CommandBuffer) VertexArray(offset, nComps, stride int) {
	cb.appendInts(RendererVertexArray, offset, nComps, stride)
}

// DisableVertexArray adds a command to the command buffer to disable the
// current vertex array.
func (cb *CommandBuffer) DisableVertexArray() {
	cb.appendInts(RendererDisableVertexArray)
}

// ColorArray adds a command to the command buffer that specifies an array
// of RGB per-vertex colors to use for a subsequent draw command. Its
// arguments are analogous to the ones passed to VertexArray.
func (cb *CommandBuffer) ColorArray(offset, nComps, stride int) {
	cb.appendInts(RendererColorArray, offset, nComps, stride)
}

// DisableColorArray adds a command to the command buffer that disables
// the current array of RGB per-vertex colors.
func (cb *CommandBuffer) DisableColorArray() {
	cb.appendInts(RendererDisableColorArray)
}

// TexCoordArray adds a command to the command buffer that specifies an
// array of per-vertex texture coordinates. Its arguments are analogous
// to the ones passed to VertexArray.
func (cb *CommandBuffer) TexCoordArray(offset, nComps, stride int) {
	cb.appendInts(RendererTexCoordArray, offset, nComps, stride)
}

// DisableTexCoordArray adds a command to the command buffer that disables
// the currently-active array of texture coordinates.
func (cb *CommandBuffer) DisableTexCoordArray() {
	cb.appendInts(RendererDisableTexCoordArray)
}

// PointSize adds a command to the command buffer that specifies the size
// of subsequent points that are drawn in pixels.
func (cb *CommandBuffer) PointSize(w float32) {
	cb.appendInts(RendererPointSize)
	// Scale as needed so that points are the same size on retina-style displays.
	cb.appendFloats(w * dpiScale(platform))
}

// DrawPoints adds a command to the command buffer to draw a number of points.
// offset gives the offset in the command buffer where the vertex indices for
// the points begin (as returned by e.g., the IntBuffer method) and count is
// the number of points to draw.
func (cb *CommandBuffer) DrawPoints(offset, count int) {
	cb.appendInts(RendererDrawPoints, offset, count)
}

// LineWidth adds a command to the command buffer that sets the width in
// pixels of subsequent lines that are drawn.
func (cb *CommandBuffer) LineWidth(w float32) {
	cb.appendInts(RendererLineWidth)
	// Scale as needed so that lines are the same width on retina-style displays.
	cb.appendFloats(w * dpiScale(platform))
}

// DrawLines adds a command to the command buffer to draw a number of
// lines; each line is specified by two indices in the index buffer.
// offset gives the offset in the current command buffer where the index
// buffer is (e.g., as returned by IntBuffer), and count gives the total
// number of vertices in the vertex buffer.
func (cb *CommandBuffer) DrawLines(offset, count int) {
	cb.appendInts(RendererDrawLines, offset, count)
}

// DrawTriangles adds a command to the command buffer to draw a number of
// triangles; each is specified by three vertices in the index
// buffer. offset gives the offset to the start of the index buffer in the
// current command buffer and count gives the total number of indices.
func (cb *CommandBuffer) DrawTriangles(offset, count int) {
	cb.appendInts(RendererDrawTriangles, offset, count)
}

// DrawTriangles adds a command to the command buffer to draw a number of
// quads; each is specified by four vertices in the index buffer. offset
// gives the offset to the start of the index buffer in the current command
// buffer and count gives the total number of indices.
func (cb *CommandBuffer) DrawQuads(offset, count int) {
	cb.appendInts(RendererDrawQuads, offset, count)
}

// Call adds a command to the command buffer that causes the commands in
// the provided command buffer to be processed and executed. After the end
// of the command buffer is reached, processing of command in the current
// command buffer continues.
func (cb *CommandBuffer) Call(sub CommandBuffer) {
	if sub.buf == nil {
		// make it a no-op
		return
	}
	up := uintptr(unsafe.Pointer(&sub.buf[0]))
	cb.appendInts(RendererCallBuffer, int(up&0xffffffff), int(up>>32), len(sub.buf))
}

// ResetState adds a command to the comment buffer that resets all of the
// assorted graphics state (scissor rectangle, blending, texturing, vertex
// arrays, etc.) to default values.
func (cb *CommandBuffer) ResetState() {
	cb.appendInts(RendererResetState)
}

///////////////////////////////////////////////////////////////////////////
// DrawBuilders

// The various *DrawBuilder classes provide capabilities for specifying a
// number of independent things of the same type to draw and then
// generating corresponding buffer storage and draw commands in a
// CommandBuffer. This allows batching up many things to be drawn all in a
// single draw command, with corresponding GPU performance benefits.

// PointsDrawBuilder accumulates colored points to be drawn.
type PointsDrawBuilder struct {
	p       [][2]float32
	color   []RGB
	indices []int32
}

// Reset resets all of the internal storage in the PointsDrawBuilder so that
// new points can be specified. It maintains the memory allocations so that
// once the system reaches steady state, there will generally not be dynamic
// memory allocations when it is used.
func (p *PointsDrawBuilder) Reset() {
	p.p = p.p[:0]
	p.color = p.color[:0]
	p.indices = p.indices[:0]
}

// AddPoint adds the specified point to the draw list in the
// PointsDrawBuilder.
func (p *PointsDrawBuilder) AddPoint(pt [2]float32, color RGB) {
	p.p = append(p.p, pt)
	p.color = append(p.color, color)
	p.indices = append(p.indices, int32(len(p.p)-1))
}

// Bounds returns the 2D bounding box of all of the points provided to the
// PointsDrawBuilder.
func (p *PointsDrawBuilder) Bounds() Extent2D {
	return Extent2DFromPoints(p.p)
}

// GenerateCommands adds a draw command for all of the points in the
// PointsDrawBuilder to the provided command buffer.
func (p *PointsDrawBuilder) GenerateCommands(cb *CommandBuffer) {
	if len(p.indices) == 0 {
		return
	}

	// Create arrays for the vertex positions and colors.
	pi := cb.Float2Buffer(p.p)
	cb.VertexArray(pi, 2, 2*4)
	rgb := cb.RGBBuffer(p.color)
	cb.ColorArray(rgb, 3, 3*4)

	// Create an index buffer from the indices.
	ind := cb.IntBuffer(p.indices)

	// Add the draw command to the command buffer.
	cb.DrawPoints(ind, len(p.indices))
}

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

// AddPolyline adds multiple lines to the lines draw builder where the
// vertex positions of the liens are found by adding each vertex of the
// provided shape array to the center point p.
func (l *LinesDrawBuilder) AddPolyline(p [2]float32, shape [][2]float32) {
	idx := int32(len(l.p))
	for _, delta := range shape {
		pp := add2f(p, delta)
		l.p = append(l.p, pp)
	}
	for i := 0; i < len(shape); i++ {
		l.indices = append(l.indices, idx+int32(i), idx+int32((i+1)%len(shape)))
	}
}

// Bounds returns the 2D bounding box of the specified lines.
func (l *LinesDrawBuilder) Bounds() Extent2D {
	return Extent2DFromPoints(l.p)
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
}

// ColoredLinesDrawBuilder is similar to the LinesDrawBuilder though it
// allows specifying the color of each line individually.  Its methods
// otherwise mostly parallel those of LinesDrawBuilder; see the
// documentation there.
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

var (
	// So that we can efficiently draw circles with various tessellations,
	// circlePoints caches vertex positions of a unit circle at the origin
	// for specified tessellation rates.
	circlePoints map[int][][2]float32
)

// getCirclePoints returns the vertices for a unit circle at the origin
// with the given number of segments; it creates the vertex slice if this
// tessellation rate hasn't been seen before and otherwise returns a
// preexisting one.
func getCirclePoints(nsegs int) [][2]float32 {
	if circlePoints == nil {
		circlePoints = make(map[int][][2]float32)
	}
	if _, ok := circlePoints[nsegs]; !ok {
		// Evaluate the vertices of the circle to initialize a new slice.
		var pts [][2]float32
		for d := 0; d < nsegs; d++ {
			angle := radians(float32(d) / float32(nsegs) * 360)
			pt := [2]float32{sin(angle), cos(angle)}
			pts = append(pts, pt)
		}
		circlePoints[nsegs] = pts
	}

	// One way or another, it's now available in the map.
	return circlePoints[nsegs]
}

// AddCircle adds lines that draw the outline of a circle with specified radius and color
// centered at the specified point p. The nsegs parameter specifies the tessellation rate
// for the circle.
func (l *ColoredLinesDrawBuilder) AddCircle(p [2]float32, radius float32, nsegs int, color RGB) {
	circle := getCirclePoints(nsegs)

	idx := int32(len(l.p))
	for i := 0; i < nsegs; i++ {
		// Translate the points to be centered around the point p with the
		// given radius and add them to the vertex buffer.
		pi := [2]float32{p[0] + radius*circle[i][0], p[1] + radius*circle[i][1]}
		l.p = append(l.p, pi)
		l.color = append(l.color, color)
	}
	for i := 0; i < nsegs; i++ {
		// Initialize the index buffer; note that the first vertex is
		// reused as the endpoint of the last line segment.
		l.indices = append(l.indices, idx+int32(i), idx+int32((i+1)%nsegs))
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
	circle := getCirclePoints(nsegs)

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

// TextDrawBuilder accumulates text to be drawn, batching it up in a single
// draw command.
type TextDrawBuilder struct {
	// Buffers for regular text
	p       [][2]float32
	uv      [][2]float32
	rgb     []RGB
	indices []int32

	// Buffers for background quads, if specified
	bgp       [][2]float32
	bgrgb     []RGB
	bgindices []int32
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
	// Initial state; start out pixel-perfect, at least.
	x0, y0 := float32(int(p[0]+0.5)), float32(int(p[1]+0.5))
	// Current cursor position
	px, py := x0, y0

	for i := range text {
		style := styles[i]

		// Total between subsequent lines, vertically.
		dy := float32(style.Font.size + style.LineSpacing)

		// Bounds for the current line's background box, if needed
		bx0, by0 := px, py

		// Utility function that is called at the end of each line when
		// DrawBackground is enabled; this takes the accumulated bounding
		// box of the current line and emits a background quad for it.
		flushbg := func() {
			// Second corner of the bounding box
			bx1, by1 := px, py-dy

			// Add a quad to stake out the background for this line.
			startIdx := int32(len(td.bgp))
			color := style.BackgroundColor
			td.bgrgb = append(td.bgrgb, color, color, color, color)
			// Additional padding
			padx, pady := float32(1), float32(0)
			// Emit the four vertices of the line bound, padded.
			td.bgp = append(td.bgp, [][2]float32{
				{bx0 - padx, by0 - pady},
				{bx1 + padx, by0 - pady},
				{bx1 + padx, by1 + pady},
				{bx0 - padx, by1 + pady}}...)
			td.bgindices = append(td.bgindices, startIdx, startIdx+1, startIdx+2, startIdx+3)
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
				px = x0
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
				// Get the vertex positions and texture coordinates for the
				// glyph.
				u0, v0, u1, v1 := glyph.U0, glyph.V0, glyph.U1, glyph.V1
				x0, y0, x1, y1 := glyph.X0, glyph.Y0, glyph.X1, glyph.Y1

				// Add the quad for the glyph to the vertex/index buffers
				startIdx := int32(len(td.p))
				td.uv = append(td.uv, [][2]float32{{u0, v0}, {u1, v0}, {u1, v1}, {u0, v1}}...)
				td.rgb = append(td.rgb, style.Color, style.Color, style.Color, style.Color)
				td.p = append(td.p, [][2]float32{
					{px + x0, py - y0},
					{px + x1, py - y0},
					{px + x1, py - y1},
					{px + x0, py - y1}}...)
				td.indices = append(td.indices, startIdx, startIdx+1, startIdx+2, startIdx+3)
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
	td.p = td.p[:0]
	td.uv = td.uv[:0]
	td.rgb = td.rgb[:0]
	td.indices = td.indices[:0]
	td.bgp = td.bgp[:0]
	td.bgrgb = td.bgrgb[:0]
	td.bgindices = td.bgindices[:0]
}

func (td *TextDrawBuilder) GenerateCommands(cb *CommandBuffer) {
	// Issue the commands to draw the background first, if any background
	// quads have been specified.
	if len(td.bgindices) > 0 {
		p := cb.Float2Buffer(td.bgp)
		cb.VertexArray(p, 2, 2*4)

		rgb := cb.RGBBuffer(td.bgrgb)
		cb.ColorArray(rgb, 3, 3*4)

		ind := cb.IntBuffer(td.bgindices)
		cb.DrawQuads(ind, len(td.bgindices))
	}

	// Issue the drawing commands for the text itself.
	if len(td.indices) > 0 {
		// Enable blending so that we get antialiasing at character edges
		// (which have fractional alpha in the atlas texture.)
		cb.Blend()

		// Enable the texture with the font atlas
		texid := uint32(imgui.CurrentIO().Fonts().GetTextureID())
		cb.EnableTexture(texid)

		// Enable the assorted vertex buffers.
		p := cb.Float2Buffer(td.p)
		cb.VertexArray(p, 2, 2*4)

		rgb := cb.RGBBuffer(td.rgb)
		cb.ColorArray(rgb, 3, 3*4)

		uv := cb.Float2Buffer(td.uv)
		cb.TexCoordArray(uv, 2, 2*4)

		// Enable the index buffer and issue the draw command.
		ind := cb.IntBuffer(td.indices)
		cb.DrawQuads(ind, len(td.indices))

		// Clean up after ourselves.
		cb.DisableTexture()
		cb.DisableBlend()
	}
}

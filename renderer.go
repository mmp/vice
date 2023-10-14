// renderer.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"image"
	"math"
	"sync"
	"unsafe"

	"github.com/mmp/imgui-go/v4"
	"golang.org/x/exp/slog"
)

// Renderer defines an interface for all of the various drawing that happens in vice.
// There is currently a single implementation of it--OpenGL2Renderer--though having
// all of these details behind the Renderer interface would make it realtively easy
// to write a Vulkan, Metal, or DirectX rendering backend.
type Renderer interface {
	// CreateTextureFromImage returns an identifier for a texture map defined
	// by the specified image.
	CreateTextureFromImage(image image.Image) uint32

	// CreateTextureFromImage returns an identifier for a texture map defined
	// by the specified image pyramid.
	CreateTextureFromImages(image []image.Image) uint32

	// UpdateTextureFromImage updates the contents of an existing texture
	// with the provided image.
	UpdateTextureFromImage(id uint32, image image.Image)

	// UpdateTextureFromImage updates the contents of an existing texture
	// with the provided image pyramid.
	UpdateTextureFromImages(id uint32, image []image.Image)

	// DestroyTexture frees the resources associated with the given texture id.
	DestroyTexture(id uint32)

	// RenderCommandBuffer executes all of the commands encoded in the
	// provided command buffer, returning statistics about what was
	// rendered.
	RenderCommandBuffer(*CommandBuffer) RendererStats

	GetPointsTextureId() uint32

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

func (rs RendererStats) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("buffers", rs.nBuffers),
		slog.Int("buffer_memory", rs.bufferBytes),
		slog.Int("draw_calls", rs.nDrawCalls),
		slog.Int("points_drawn", rs.nPoints),
		slog.Int("lines", rs.nLines),
		slog.Int("tris", rs.nTriangles),
		slog.Int("quads", rs.nQuads),
	)
}

///////////////////////////////////////////////////////////////////////////

// The command buffer stores a series of rendering commands, represented by
// the following values. Each one is followed in the buffer by a number of
// command arguments, after which the next command follows.  Comments
// after each command briefly describe its arguments.
//
// Buffers (vertex, index, color, texcoord), are all stored directly in the
// CommandBuffer, following the RendererDataBuffer command; the first
// argument after those commands is the length of the buffer in int32s and
// then its values follow directly. Rendering commands that use buffers
// (e.g., buffer binding commands like RendererVertexArray or draw commands
// like RendererDrawLines) are then directed to those buffers via integer
// parameters that encode the offset from the start of the command buffer
// where a buffer begins. (Note that this implies that one CommandBuffer
// cannot refer to a vertex/index buffer in another CommandBuffer.

const (
	RendererLoadProjectionMatrix = iota // 16 float32: matrix
	RendererLoadModelViewMatrix         // 16 float32: matrix
	RendererClearRGBA                   // 4 float32: RGBA
	RendererScissor                     // 4 int32: x, y, width, height
	RendererViewport                    // 4 int32: x, y, width, height
	RendererBlend                       // no args: for now always src alpha, 1-src alpha
	RendererSetRGBA                     // 4 float32: RGBA
	RendererDisableBlend                // no args
	RendererDataBuffer                  // int32 size for skip ahead, then size*uint32 values
	RendererEnableTexture               // int32 handle
	RendererDisableTexture              // no args
	RendererVertexArray                 // byte offset to array value, size (bytes), n components, stride (bytes)
	RendererDisableVertexArray          // no args
	RendererRGB8Array                   // byte offset to array values, size (bytes), n components, stride (bytes)
	RendererRGB32Array                  // byte offset to array values, size (bytes), n components, stride (bytes)
	RendererDisableColorArray           // no args
	RendererTexCoordArray               // byte offset to array values, size (bytes), n components, stride (bytes)
	RendererDisableTexCoordArray        // no args
	RendererDrawPoints                  // 3 int32: offset to the index buffer, size (bytes), count
	RendererDrawLines                   // 3 int32: offset to the index buffer, size (bytes), count
	RendererDrawTriangles               // 3 int32: offset to the index buffer, size (bytes), count
	RendererCallBuffer                  // 1 int32: buffer index
	RendererResetState                  // no args
	RendererStartRTT                    // 2 int32 (res)
	RendererEndRTT                      // no args
	RendererApplyCRT                    // no args
)

// CommandBuffer encodes a sequence of rendering commands in an
// API-agnostic manner. It makes it possible for other parts of vice to
// "pre-bake" rendering work into a form that can be efficiently processed
// by a Renderer and possibly reused over multiple frames.
type CommandBuffer struct {
	Buf    []uint32
	called []CommandBuffer
}

// CommandBuffers are managed using a sync.Pool so that their buf slice
// allocations persist across multiple uses.
var commandBufferPool = sync.Pool{New: func() any { return &CommandBuffer{} }}

func GetCommandBuffer() *CommandBuffer {
	return commandBufferPool.Get().(*CommandBuffer)
}

func ReturnCommandBuffer(cb *CommandBuffer) {
	cb.Reset()
	commandBufferPool.Put(cb)
}

// Reset resets the command buffer's length to zero so that it can be
// reused.
func (cb *CommandBuffer) Reset() {
	cb.Buf = cb.Buf[:0]
	cb.called = cb.called[:0]
}

// growFor ensures that at least n more values can be added to the end of
// the buffer without going past its capacity.
func (cb *CommandBuffer) growFor(n int) {
	if len(cb.Buf)+n > cap(cb.Buf) {
		sz := 2 * cap(cb.Buf)
		if sz < 1024 {
			sz = 1024
		}
		if sz < len(cb.Buf)+n {
			sz = 2 * (len(cb.Buf) + n)
		}
		b := make([]uint32, len(cb.Buf), sz)
		copy(b, cb.Buf)
		cb.Buf = b
	}
}

func (cb *CommandBuffer) appendFloats(floats ...float32) {
	for _, f := range floats {
		// Convert each one to a uint32 since that's the type that is
		// actually stored...
		cb.Buf = append(cb.Buf, math.Float32bits(f))
	}
}

func (cb *CommandBuffer) appendInts(ints ...int) {
	for _, i := range ints {
		if i != int(uint32(i)) {
			lg.Errorf("%d: attempting to add non-32-bit value to CommandBuffer", i)
		}
		cb.Buf = append(cb.Buf, uint32(i))
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
	if start%4 != 0 {
		lg.Errorf("%d: unaligned offset passed to FloatSlice", start)
	}
	ptr := (*float32)(unsafe.Pointer(&cb.Buf[start/4]))
	return unsafe.Slice(ptr, length)
}

func (cb *CommandBuffer) LoadProjectionMatrix(m Matrix3) {
	cb.appendInts(RendererLoadProjectionMatrix)
	cb.appendFloats(
		m[0][0], m[1][0], 0, m[2][0],
		m[0][1], m[1][1], 0, m[2][1],
		0, 0, 1, 0,
		m[0][2], m[1][2], 0, m[2][2])
}

func (cb *CommandBuffer) LoadModelViewMatrix(m Matrix3) {
	cb.appendInts(RendererLoadModelViewMatrix)
	cb.appendFloats(
		m[0][0], m[1][0], 0, m[2][0],
		m[0][1], m[1][1], 0, m[2][1],
		0, 0, 1, 0,
		m[0][2], m[1][2], 0, m[2][2])
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

// SetDrawBounds sets the scissor rectangle and viewport according to the
// specified bounds so that subsequent code can assume window (or Pane)
// coordinates from (0,0)-(width,height) when drawing things.
func (cb *CommandBuffer) SetDrawBounds(b Extent2D) {
	// One messy detail here is that these windows are specified in
	// framebuffer coordinates, not display coordinates, so they must be
	// scaled for e.g., retina displays.
	scale := platform.FramebufferSize()[1] / platform.DisplaySize()[1]
	x0, y0 := int(scale*b.p0[0]), int(scale*b.p0[1])
	w, h := int(scale*b.Width()), int(scale*b.Height())
	cb.Scissor(x0, y0, w, h)
	cb.Viewport(x0, y0, w, h)
}

// SetRGBA adds a command to the command buffer to set the current RGBA
// color. Subsequent draw commands will inherit this color unless they
// specify e.g., per-vertex colors themselves.
func (cb *CommandBuffer) SetRGBA(rgba RGBA) {
	cb.appendInts(RendererSetRGBA)
	cb.appendFloats(rgba.R, rgba.G, rgba.B, rgba.A)
}

// SetRGB adds a command to the command buffer to set the current RGB
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

// Float2Buffer stores the length of the provided slice of [2]float32
// values and then the values themselves in the CommandBuffer and returns
// the byte offset where the length of the slice is stored; this offset can
// then be passed to commands like VertexArray to specify this array.

type RenderBuffer struct {
	// both are in bytes
	Offset int
	Size   int
}

/*
type RenderBufferView struct {
	Offset      int
	Stride      int
	NComponents int
	Bits        int
}
*/

func (cb *CommandBuffer) AddFloat2Buffer(buf [][2]float32) RenderBuffer {
	return addBuffer(cb, buf)
}

func (cb *CommandBuffer) AddInt32Buffer(buf []int32) RenderBuffer {
	return addBuffer(cb, buf)
}

func (cb *CommandBuffer) AddRGB32Buffer(buf []RGB) RenderBuffer {
	return addBuffer(cb, buf)
}

func (cb *CommandBuffer) AddByteBuffer(buf []byte) RenderBuffer {
	return addBuffer(cb, buf)
}

func addBuffer[T any](cb *CommandBuffer, buf []T) RenderBuffer {
	nBytes := int(unsafe.Sizeof(buf[0])) * len(buf)
	nInt32s := (nBytes + 3) / 4
	cb.appendInts(RendererDataBuffer, nInt32s)
	dataByteOffset := 4 * len(cb.Buf)

	cb.growFor(nInt32s)
	start := len(cb.Buf)
	cb.Buf = cb.Buf[:start+nInt32s]
	copy(cb.Buf[start:start+nInt32s], unsafe.Slice((*uint32)(unsafe.Pointer(&buf[0])), nInt32s))

	return RenderBuffer{Offset: dataByteOffset, Size: nBytes}
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
func (cb *CommandBuffer) VertexArray(b RenderBuffer, nComps, stride int) {
	cb.appendInts(RendererVertexArray, b.Offset, b.Size, nComps, stride)
}

// DisableVertexArray adds a command to the command buffer to disable the
// current vertex array.
func (cb *CommandBuffer) DisableVertexArray() {
	cb.appendInts(RendererDisableVertexArray)
}

// ColorArray adds a command to the command buffer that specifies an array
// of float32 RGB colors to use for a subsequent draw command. Its
// arguments are analogous to the ones passed to VertexArray.
func (cb *CommandBuffer) RGB32Array(b RenderBuffer, nComps, stride int) {
	cb.appendInts(RendererRGB32Array, b.Offset, b.Size, nComps, stride)
}

// ColorArray adds a command to the command buffer that specifies an array
// of 8-bit RGBA colors to use for a subsequent draw command. Its arguments
// are analogous to the ones passed to VertexArray.
func (cb *CommandBuffer) RGB8Array(b RenderBuffer, nComps, stride int) {
	cb.appendInts(RendererRGB8Array, b.Offset, b.Size, nComps, stride)
}

// DisableColorArray adds a command to the command buffer that disables
// the current array of RGB per-vertex colors.
func (cb *CommandBuffer) DisableColorArray() {
	cb.appendInts(RendererDisableColorArray)
}

// TexCoordArray adds a command to the command buffer that specifies an
// array of per-vertex texture coordinates. Its arguments are analogous
// to the ones passed to VertexArray.
func (cb *CommandBuffer) TexCoordArray(b RenderBuffer, nComps, stride int) {
	cb.appendInts(RendererTexCoordArray, b.Offset, b.Size, nComps, stride)
}

// DisableTexCoordArray adds a command to the command buffer that disables
// the currently-active array of texture coordinates.
func (cb *CommandBuffer) DisableTexCoordArray() {
	cb.appendInts(RendererDisableTexCoordArray)
}

// DrawPoints adds a command to the command buffer to draw a number of points.
// offset gives the offset in the command buffer where the vertex indices for
// the points begin (as returned by e.g., the IntBuffer method) and count is
// the number of points to draw.
func (cb *CommandBuffer) DrawPoints(b RenderBuffer, count int) {
	cb.appendInts(RendererDrawPoints, b.Offset, b.Size, count)
}

// DrawLines adds a command to the command buffer to draw a number of
// lines; each line is specified by two indices in the index buffer.
// offset gives the offset in the current command buffer where the index
// buffer is (e.g., as returned by IntBuffer), and count gives the total
// number of vertices in the vertex buffer.
func (cb *CommandBuffer) DrawLines(indices RenderBuffer, count int) {
	cb.appendInts(RendererDrawLines, indices.Offset, indices.Size, count)
}

// DrawTriangles adds a command to the command buffer to draw a number of
// triangles; each is specified by three vertices in the index
// buffer. offset gives the offset to the start of the index buffer in the
// current command buffer and count gives the total number of indices.
func (cb *CommandBuffer) DrawTriangles(indices RenderBuffer, count int) {
	cb.appendInts(RendererDrawTriangles, indices.Offset, indices.Size, count)
}

// Call adds a command to the command buffer that causes the commands in
// the provided command buffer to be processed and executed. After the end
// of the command buffer is reached, processing of command in the current
// command buffer continues.
func (cb *CommandBuffer) Call(sub CommandBuffer) {
	if sub.Buf == nil {
		// make it a no-op
		return
	}

	cb.appendInts(RendererCallBuffer, len(cb.called))
	// Make our own copy of the slice to ensure it isn't garbage collected.
	cb.called = append(cb.called, sub)
}

// ResetState adds a command to the comment buffer that resets all of the
// assorted graphics state (scissor rectangle, blending, texturing, vertex
// arrays, etc.) to default values.
func (cb *CommandBuffer) ResetState() {
	cb.appendInts(RendererResetState)
}

func (cb *CommandBuffer) StartRTT(res [2]int) {
	cb.appendInts(RendererStartRTT, res[0], res[1])
}

func (cb *CommandBuffer) EndRTT() {
	cb.appendInts(RendererEndRTT)
}

func (cb *CommandBuffer) ApplyCRTEffect(e Extent2D) {
	cb.appendInts(RendererApplyCRT)
	cb.appendFloats(e.p0[0], e.p0[1], e.p1[0], e.p1[1])
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
	TexturedTrianglesDrawBuilder
	color []RGB
}

// Reset resets all of the internal storage in the PointsDrawBuilder so that
// new points can be specified. It maintains the memory allocations so that
// once the system reaches steady state, there will generally not be dynamic
// memory allocations when it is used.
func (p *PointsDrawBuilder) Reset() {
	p.TexturedTrianglesDrawBuilder.Reset()
	p.color = p.color[:0]
}

// AddPoint adds the specified point to the draw list in the
// PointsDrawBuilder.
func (p *PointsDrawBuilder) AddPoint(pt [2]float32, radius float32, color RGB) {
	r2 := radius / 2
	p.TexturedTrianglesDrawBuilder.AddQuad(
		add2f(pt, [2]float32{-r2, -r2}), add2f(pt, [2]float32{r2, -r2}),
		add2f(pt, [2]float32{r2, r2}), add2f(pt, [2]float32{-r2, r2}),
		[2]float32{0, 0}, [2]float32{1, 0}, [2]float32{1, 1}, [2]float32{0, 1})
	p.color = append(p.color, color, color, color, color)
}

// Bounds returns the 2D bounding box of all of the points provided to the
// PointsDrawBuilder.
func (p *PointsDrawBuilder) Bounds() Extent2D {
	return Extent2DFromPoints(p.p)
}

// GenerateCommands adds a draw command for all of the points in the
// PointsDrawBuilder to the provided command buffer.
func (p *PointsDrawBuilder) GenerateCommands(cb *CommandBuffer) {
	if len(p.color) == 0 {
		return
	}

	rgbBuf := cb.AddRGB32Buffer(p.color)
	cb.RGB32Array(rgbBuf, 3, 3*4)
	p.TexturedTrianglesDrawBuilder.GenerateCommands(renderer.GetPointsTextureId(), cb)

	cb.DisableColorArray()
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
// vertex positions of the lines are found by adding each vertex of the
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

// AddCircle adds lines that draw the outline of a circle with specified
// and color centered at the specified point p. The nsegs parameter
// specifies the tessellation rate for the circle.
func (l *LinesDrawBuilder) AddCircle(p [2]float32, radius float32, nsegs int) {
	circle := getCirclePoints(nsegs)

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

func (l *LinesDrawBuilder) AddLatLongCircle(p Point2LL, nmPerLongitude float32, r float32, nsegs int) {
	// We want vertices in lat-long space but will draw the circle in
	// nm space since distance is uniform there.
	pc := ll2nm(p, nmPerLongitude)
	for i := 0; i < nsegs; i++ {
		pt := func(i int) [2]float32 {
			a := float32(i) / float32(nsegs) * 2 * math.Pi
			v := [2]float32{sin(a), cos(a)}
			v = scale2f(v, r)
			return nm2ll(add2f(pc, v), nmPerLongitude)
		}
		l.AddLine(pt(i), pt(i+1))
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

	cb.VertexArray(cb.AddFloat2Buffer(l.p), 2, 2*4)

	indBuf := cb.AddInt32Buffer(l.indices)
	cb.DrawLines(indBuf, len(l.indices))

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

func (l *ColoredLinesDrawBuilder) AddPolyline(p [2]float32, color RGB, shape [][2]float32) {
	l.LinesDrawBuilder.AddPolyline(p, shape)
	for range shape {
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

func (l *ColoredLinesDrawBuilder) GenerateCommands(cb *CommandBuffer) {
	if len(l.indices) == 0 {
		return
	}

	cb.VertexArray(cb.AddFloat2Buffer(l.p), 2, 2*4)
	cb.RGB32Array(cb.AddRGB32Buffer(l.color), 2, 3*4)

	indBuf := cb.AddInt32Buffer(l.indices)
	cb.DrawLines(indBuf, len(l.indices))

	cb.DisableVertexArray()
	cb.DisableColorArray()
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

	pBuf := cb.AddFloat2Buffer(t.p)
	cb.VertexArray(pBuf, 2, 2*4)

	indBuf := cb.AddInt32Buffer(t.indices)
	cb.DrawTriangles(indBuf, len(t.indices))

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

	rgbBuf := cb.AddRGB32Buffer(t.color)
	cb.RGB32Array(rgbBuf, 3, 3*4)

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

func (t *TexturedTrianglesDrawBuilder) GenerateCommands(texid uint32, cb *CommandBuffer) {
	if len(t.indices) == 0 {
		return
	}

	cb.Blend() // alpha blending...
	cb.EnableTexture(texid)

	uvBuf := cb.AddFloat2Buffer(t.uv)
	cb.TexCoordArray(uvBuf, 2, 2*4)

	t.TrianglesDrawBuilder.GenerateCommands(cb)

	cb.DisableTexCoordArray()
	cb.DisableTexture()
	cb.DisableBlend()
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
	regular, shadow TextBuffers

	// Buffers for background quads, if specified
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
		add2f(p, [2]float32{x0, -y0}),
		add2f(p, [2]float32{x1, -y0}),
		add2f(p, [2]float32{x1, -y1}),
		add2f(p, [2]float32{x0, -y1})}...)
	t.indices = append(t.indices, startIdx, startIdx+1, startIdx+2)
	t.indices = append(t.indices, startIdx, startIdx+2, startIdx+3)
}

func (t *TextBuffers) GenerateCommands(cb *CommandBuffer) {
	if len(t.indices) == 0 {
		return
	}

	// Enable the assorted vertex buffers.
	pBuf := cb.AddFloat2Buffer(t.p)
	cb.VertexArray(pBuf, 2, 2*4)
	rgbBuf := cb.AddRGB32Buffer(t.rgb)
	cb.RGB32Array(rgbBuf, 3, 3*4)
	uvBuf := cb.AddFloat2Buffer(t.uv)
	cb.TexCoordArray(uvBuf, 2, 2*4)

	// Enable the index buffer and issue the draw command.
	indBuf := cb.AddInt32Buffer(t.indices)
	cb.DrawTriangles(indBuf, len(t.indices))
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
	// DropShadow controls whether a drop shadow of the text is drawn,
	// offset one pixel to the right and one pixel down from the main text.
	DropShadow bool
	// DropShadowColor specifies the color to use for drop shadow text.
	DropShadowColor RGB
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
			td.background.indices = append(td.background.indices, startIdx, startIdx+1, startIdx+2)
			td.background.indices = append(td.background.indices, startIdx, startIdx+2, startIdx+3)
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
				td.regular.Add([2]float32{px, py}, glyph, style.Color)

				if style.DropShadow {
					td.shadow.Add([2]float32{px + 1, py - 1}, glyph, style.DropShadowColor)
				}
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
	td.regular.Reset()
	td.shadow.Reset()

	td.background.p = td.background.p[:0]
	td.background.rgb = td.background.rgb[:0]
	td.background.indices = td.background.indices[:0]
}

func (td *TextDrawBuilder) GenerateCommands(cb *CommandBuffer) {
	if len(td.regular.indices) == 0 {
		return
	}

	// Issue the commands to draw the background first, if any background
	// quads have been specified.
	if len(td.background.indices) > 0 {
		pBuf := cb.AddFloat2Buffer(td.background.p)
		cb.VertexArray(pBuf, 2, 2*4)

		rgbBuf := cb.AddRGB32Buffer(td.background.rgb)
		cb.RGB32Array(rgbBuf, 3, 3*4)

		indBuf := cb.AddInt32Buffer(td.background.indices)
		cb.DrawTriangles(indBuf, len(td.background.indices))
	}

	// Issue the drawing commands for the text itself.

	// Enable blending so that we get antialiasing at character edges
	// (which have fractional alpha in the atlas texture.)
	cb.Blend()

	// Enable the texture with the font atlas
	texid := uint32(imgui.CurrentIO().Fonts().GetTextureID())
	cb.EnableTexture(texid)

	// Draw the drop shadows before the main text
	td.shadow.GenerateCommands(cb)
	td.regular.GenerateCommands(cb)

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

///////////////////////////////////////////////////////////////////////////
// imgui draw list conversion

// GenerateImguiCommandBuffer retrieves the imgui draw list for the current
// frame and emits corresponding commands to the provided CommandBuffer.
func GenerateImguiCommandBuffer(cb *CommandBuffer) {
	displaySize := platform.DisplaySize()
	framebufferSize := platform.FramebufferSize()
	drawData := imgui.RenderedDrawData()

	// Avoid rendering when minimized.
	displayWidth, displayHeight := displaySize[0], displaySize[1]
	fbWidth, fbHeight := framebufferSize[0], framebufferSize[1]
	if (fbWidth <= 0) || (fbHeight <= 0) {
		return
	}

	// Scale coordinates for retina displays (screen coordinates !=
	// framebuffer coordinates)
	drawData.ScaleClipRects(imgui.Vec2{
		X: fbWidth / displayWidth,
		Y: fbHeight / displayHeight,
	})

	cb.ResetState()

	// Setup viewport, orthographic projection matrix.  Our visible imgui
	// space lies from draw_data->DisplayPos (top left) to
	// draw_data->DisplayPos+data_data->DisplaySize (bottom right).
	// DisplayMin is typically (0,0) for single viewport apps.
	cb.LoadProjectionMatrix(Identity3x3().Ortho(0, float32(displayWidth), float32(displayHeight), 0))
	cb.LoadModelViewMatrix(Identity3x3())
	cb.Viewport(0, 0, int(fbWidth), int(fbHeight))
	cb.Blend()

	// Get the vertex and index buffer sizes and layout information.
	vertexSize, vertexOffsetPos, vertexOffsetUV, vertexOffsetRGB := imgui.VertexBufferLayout()
	indexSize := imgui.IndexBufferLayout()

	// Handle each command list
	for _, commandList := range drawData.CommandLists() {
		vertexBufferPtr, vertexBufferSizeBytes := commandList.VertexBuffer()
		indexBufferPtr, indexBufferSizeBytes := commandList.IndexBuffer()

		// CommandBuffer only supports int32 for index buffers, so in the
		// usual case that imgui has given uint16s, create a corresponding
		// new int32 buffer.
		if indexSize != 4 {
			n := indexBufferSizeBytes / indexSize
			buf16 := unsafe.Slice((*uint16)(indexBufferPtr), n)

			buf32 := make([]int32, n)
			for i := 0; i < n; i++ {
				buf32[i] = int32(buf16[i])
			}
			indexBufferPtr = unsafe.Pointer(&buf32[0])
			indexBufferSizeBytes = 4 * n
		}
		indexSlice := unsafe.Slice((*int32)(indexBufferPtr), indexBufferSizeBytes/4)
		indexBuf := cb.AddInt32Buffer(indexSlice)

		vertexBuf := cb.AddByteBuffer(unsafe.Slice((*byte)(vertexBufferPtr), vertexBufferSizeBytes))

		cb.VertexArray(RenderBuffer{Offset: vertexBuf.Offset + vertexOffsetPos,
			Size: vertexBuf.Size - vertexOffsetPos}, 2, vertexSize)
		cb.TexCoordArray(RenderBuffer{Offset: vertexBuf.Offset + vertexOffsetUV,
			Size: vertexBuf.Size - vertexOffsetUV}, 2, vertexSize)
		cb.RGB8Array(RenderBuffer{Offset: vertexBuf.Offset + vertexOffsetRGB,
			Size: vertexBuf.Size - vertexOffsetRGB}, 4, vertexSize)

		for _, command := range commandList.Commands() {
			if command.HasUserCallback() {
				lg.Error("Unexpected user callback in imgui draw list")
			} else {
				clipRect := command.ClipRect()
				cb.Scissor(int(clipRect.X), int(fbHeight)-int(clipRect.W),
					int(clipRect.Z-clipRect.X), int(clipRect.W-clipRect.Y))
				cb.EnableTexture(uint32(command.TextureID()))
				cb.DrawTriangles(indexBuf, command.ElementCount())
			}

			indexBuf.Offset += command.ElementCount() * 4
			indexBuf.Size -= command.ElementCount() * 4
		}
	}

	cb.DisableBlend()
	cb.ResetState()
}

// pkg/renderer/commandbuffer.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package renderer

import (
	gomath "math"
	"sync"
	"unsafe"

	"github.com/mmp/vice/pkg/math"
)

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
	RendererLoadProjectionMatrix  = iota // 16 float32: matrix
	RendererLoadModelViewMatrix          // 16 float32: matrix
	RendererClearRGBA                    // 4 float32: RGBA
	RendererScissor                      // 4 int32: x, y, width, height
	RendererViewport                     // 4 int32: x, y, width, height
	RendererBlend                        // no args: for now always src alpha, 1-src alpha
	RendererSetRGBA                      // 4 float32: RGBA
	RendererDisableBlend                 // no args
	RendererFloatBuffer                  // int32 size, then size*float32 values
	RendererIntBuffer                    // int32: size, then size*int32 values
	RendererRawBuffer                    // int32: size *in bytes*, then (3+size)/4 int32 values
	RendererEnableTexture                // int32 handle
	RendererDisableTexture               // no args
	RendererVertexArray                  // byte offset to array values, n components, stride (bytes)
	RendererDisableVertexArray           // no args
	RendererRGB8Array                    // byte offset to array values, n components, stride (bytes)
	RendererRGB32Array                   // byte offset to array values, n components, stride (bytes)
	RendererDisableColorArray            // no args
	RendererTexCoordArray                // byte offset to array values, n components, stride (bytes)
	RendererDisableTexCoordArray         // no args
	RendererLineWidth                    // float32
	RendererDrawLines                    // 2 int32: offset to the index buffer, count
	RendererDrawTriangles                // 2 int32: offset to the index buffer, count
	RendererDrawQuads                    // 2 int32: offset to the index buffer, count
	RendererCallBuffer                   // 1 int32: buffer index
	RendererResetState                   // no args
	RendererEnablePolygonStipple         // no args
	RendererDisablePolygonStipple        // no args
	RendererPolygonStipple               // 32 uint32: stipple pattern
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
		cb.Buf = append(cb.Buf, gomath.Float32bits(f))
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

func (cb *CommandBuffer) LoadProjectionMatrix(m math.Matrix3) {
	cb.appendInts(RendererLoadProjectionMatrix)
	cb.appendFloats(
		m[0][0], m[1][0], 0, m[2][0],
		m[0][1], m[1][1], 0, m[2][1],
		0, 0, 1, 0,
		m[0][2], m[1][2], 0, m[2][2])
}

func (cb *CommandBuffer) LoadModelViewMatrix(m math.Matrix3) {
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
//
// One messy detail here is that these windows are specified in framebuffer
// coordinates, not display coordinates, so they must be scaled for e.g.,
// retina displays; the scale takes care of this. It should be initialized
// as the ratio of framebuffer resolution to display resolution.
func (cb *CommandBuffer) SetDrawBounds(b math.Extent2D, scale float32) {
	x0, y0 := int(scale*b.P0[0]), int(scale*b.P0[1])
	w, h := int(scale*b.Width()), int(scale*b.Height())
	w, h = max(w, 0), max(h, 0)
	cb.Scissor(x0, y0, w, h)
	cb.Viewport(x0, y0, w, h)
}

// SetScissorBounds sets the scissor rectangle according to the
// specified bounds so that subsequent code can assume window (or Pane)
// coordinates from (0,0)-(width,height) when drawing things.
//
// One messy detail here is that these windows are specified in framebuffer
// coordinates, not display coordinates, so they must be scaled for e.g.,
// retina displays; the scale takes care of this. It should be initialized
// as the ratio of framebuffer resolution to display resolution.
func (cb *CommandBuffer) SetScissorBounds(b math.Extent2D, scale float32) {
	x0, y0 := int(scale*b.P0[0]), int(scale*b.P0[1])
	w, h := int(scale*b.Width()), int(scale*b.Height())
	w, h = max(w, 0), max(h, 0)
	cb.Scissor(x0, y0, w, h)
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

// Float2Buffer stores the provided slice of [2]float32 values in the
// CommandBuffer and returns the byte offset where the first value of the
// slice is stored; this offset can then be passed to commands like
// VertexArray to specify this array.
func (cb *CommandBuffer) Float2Buffer(buf [][2]float32) int {
	cb.appendInts(RendererFloatBuffer, 2*len(buf))
	offset := 4 * len(cb.Buf)

	n := 2 * len(buf)
	cb.growFor(n)
	start := len(cb.Buf)
	cb.Buf = cb.Buf[:start+n]
	copy(cb.Buf[start:start+n], unsafe.Slice((*uint32)(unsafe.Pointer(&buf[0])), n))

	return offset
}

// RGBBuffer stores the provided slice of RGB values in the command buffer
// and returns the byte offset where the first value of the slice is
// stored.
func (cb *CommandBuffer) RGBBuffer(buf []RGB) int {
	cb.appendInts(RendererFloatBuffer, 3*len(buf))
	offset := 4 * len(cb.Buf)

	n := 3 * len(buf)
	cb.growFor(n)
	start := len(cb.Buf)
	copy(cb.Buf[start:start+n], unsafe.Slice((*uint32)(unsafe.Pointer(&buf[0])), n))
	cb.Buf = cb.Buf[:start+n]

	return offset
}

// IntBuffer stores the provided slice of int32 values in the command buffer
// and returns the byte offset where the first value of the slice is stored.
func (cb *CommandBuffer) IntBuffer(buf []int32) int {
	cb.appendInts(RendererIntBuffer, len(buf))
	offset := 4 * len(cb.Buf)

	n := len(buf)
	cb.growFor(n)
	start := len(cb.Buf)
	copy(cb.Buf[start:start+n], unsafe.Slice((*uint32)(unsafe.Pointer(&buf[0])), n))
	cb.Buf = cb.Buf[:start+n]

	return offset
}

// RawBuffer stores the provided bytes, without further interpretation in
// the command buffer and returns the byte offset from the start of the
// buffer where they begin.
func (cb *CommandBuffer) RawBuffer(buf []byte) int {
	nints := (len(buf) + 3) / 4
	cb.appendInts(RendererRawBuffer, nints)
	offset := 4 * len(cb.Buf)

	cb.growFor(nints)
	start := len(cb.Buf)
	ptr := unsafe.Pointer(uintptr(unsafe.Pointer(&cb.Buf[0])) + uintptr(4*start))
	slice := unsafe.Slice((*byte)(ptr), len(buf))
	copy(slice, buf)
	cb.Buf = cb.Buf[:start+nints]

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
// of float32 RGB colors to use for a subsequent draw command. Its
// arguments are analogous to the ones passed to VertexArray.
func (cb *CommandBuffer) RGB32Array(offset, nComps, stride int) {
	cb.appendInts(RendererRGB32Array, offset, nComps, stride)
}

// ColorArray adds a command to the command buffer that specifies an array
// of 8-bit RGBA colors to use for a subsequent draw command. Its arguments
// are analogous to the ones passed to VertexArray.
func (cb *CommandBuffer) RGB8Array(offset, nComps, stride int) {
	cb.appendInts(RendererRGB8Array, offset, nComps, stride)
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

// LineWidth adds a command to the command buffer that sets the width in
// pixels of subsequent lines that are drawn.
func (cb *CommandBuffer) LineWidth(w float32, scale float32) {
	cb.appendInts(RendererLineWidth)
	// Scale so that lines are the same width on retina-style displays.
	cb.appendFloats(w * scale)
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

// EnablePolygonStipple adds a command to the command buffer that enables
// stipple when drawing polygons.
func (cb *CommandBuffer) EnablePolygonStipple() {
	cb.appendInts(RendererEnablePolygonStipple)
}

// DisablePolygonStipple adds a command to the command buffer that disables
// stipple when drawing polygons.
func (cb *CommandBuffer) DisablePolygonStipple() {
	cb.appendInts(RendererDisablePolygonStipple)
}

// PolygonStipple adds a command to the command buffer that specifies the
// polygon stipple pattern.
func (cb *CommandBuffer) PolygonStipple(pattern [32]uint32) {
	cb.appendInts(RendererPolygonStipple)
	cb.Buf = append(cb.Buf, pattern[:]...)
}

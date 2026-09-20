// renderer/renderer.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package renderer

import (
	"fmt"
	"image"
	"log/slog"
)

// Renderer defines an interface for all of the various drawing that happens in vice.
// There is currently a single implementation of it--OpenGL2Renderer--though having
// all of these details behind the Renderer interface would make it realtively easy
// to write a Vulkan, Metal, or DirectX rendering backend.
type Renderer interface {
	// CreateTextureFromImage returns an identifier for a texture map defined
	// by the specified image.
	CreateTextureFromImage(image image.Image, magNearest bool) uint32

	// CreateTextureFromImage returns an identifier for a texture map defined
	// by the specified image pyramid.
	CreateTextureFromImages(image []image.Image, magNearest bool) uint32

	// UpdateTextureFromImage updates the contents of an existing texture
	// with the provided image.
	UpdateTextureFromImage(id uint32, image image.Image, magNearest bool)

	// UpdateTextureFromImage updates the contents of an existing texture
	// with the provided image pyramid.
	UpdateTextureFromImages(id uint32, image []image.Image, magNearest bool)

	// DestroyTexture frees the resources associated with the given texture id.
	DestroyTexture(id uint32)

	// RenderCommandBuffer executes all of the commands encoded in the
	// provided command buffer, returning statistics about what was
	// rendered.
	RenderCommandBuffer(*CommandBuffer) Stats

	// ReadPixelRGBAs returns the RGBA colors of a rectangular region the
	// framebuffer starting at the given (x,y) coordinate with the given
	// width and height.
	ReadPixelRGBAs(x, y, width, height int) []uint8

	// Dispose releases resources allocated by the renderer.
	Dispose()
}

// Stats encapsulates assorted statistics from rendering.
type Stats struct {
	NBuffers, BufferBytes               int
	NDrawCalls                          int
	NPoints, NLines, NTriangles, NQuads int
}

func (rs *Stats) String() string {
	return fmt.Sprintf("%d buffers (%.2f MB), %d draw calls: %d points, %d lines, %d tris, %d quads",
		rs.NBuffers, float32(rs.BufferBytes)/(1024*1024), rs.NDrawCalls, rs.NPoints, rs.NLines, rs.NTriangles, rs.NQuads)
}

func (rs *Stats) Merge(s Stats) {
	rs.NBuffers += s.NBuffers
	rs.BufferBytes += s.BufferBytes
	rs.NDrawCalls += s.NDrawCalls
	rs.NPoints += s.NPoints
	rs.NLines += s.NLines
	rs.NTriangles += s.NTriangles
	rs.NQuads += s.NQuads
}

func (rs Stats) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("buffers", rs.NBuffers),
		slog.Int("buffer_memory", rs.BufferBytes),
		slog.Int("draw_calls", rs.NDrawCalls),
		slog.Int("points_drawn", rs.NPoints),
		slog.Int("lines", rs.NLines),
		slog.Int("tris", rs.NTriangles),
		slog.Int("quads", rs.NQuads),
	)
}

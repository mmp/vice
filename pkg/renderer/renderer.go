// pkg/renderer/renderer.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
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

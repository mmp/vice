// pkg/renderer/ogl2.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package renderer

import (
	"C"
	"fmt"
	"image"
	"image/draw"
	gomath "math"
	"unsafe"

	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/util"

	"github.com/go-gl/gl/v2.1/gl"
)

// Also available as a global, though only used by CommandBuffer
var lg *log.Logger

type OpenGL2Renderer struct {
	lg              *log.Logger
	createdTextures map[uint32]int
}

// NewOpenGL2Renderer creates an OpenGL context and creates a texture for the imgui fonts.
// Thus, all font creation must be finished before the renderer is created.
func NewOpenGL2Renderer(l *log.Logger) (Renderer, error) {
	lg = l

	lg.Info("Starting OpenGL2Renderer initialization")
	if err := gl.Init(); err != nil {
		return nil, fmt.Errorf("failed to initialize OpenGL: %w", err)
	}
	vendor, renderer := gl.GetString(gl.VENDOR), gl.GetString(gl.RENDERER)
	v, r := (*C.char)(unsafe.Pointer(vendor)), (*C.char)(unsafe.Pointer(renderer))
	lg.Infof("OpenGL vendor %s renderer %s", C.GoString(v), C.GoString(r))

	lg.Info("Finished OpenGL2Renderer initialization")
	return &OpenGL2Renderer{
		lg:              lg,
		createdTextures: make(map[uint32]int),
	}, nil
}

func (ogl2 *OpenGL2Renderer) Dispose() {
	for texid := range ogl2.createdTextures {
		gl.DeleteTextures(1, &texid)
	}
}

func (ogl2 *OpenGL2Renderer) createdTexture(texid uint32, bytes int) {
	_, exists := ogl2.createdTextures[texid]

	ogl2.createdTextures[texid] = bytes

	reduce := func(id uint32, bytes int, total int) int { return total + bytes }
	total := util.ReduceMap[uint32, int, int](ogl2.createdTextures, reduce, 0)
	mb := float32(total) / (1024 * 1024)

	if exists {
		ogl2.lg.Infof("Updated tex id %d: %d bytes -> %.2f MiB of textures total", texid, bytes, mb)
	} else {
		ogl2.lg.Infof("Created tex id %d: %d bytes -> %.2f MiB of textures total", texid, bytes, mb)
	}
}

func (ogl2 *OpenGL2Renderer) CreateTextureFromImage(img image.Image, magNearest bool) uint32 {
	return ogl2.CreateTextureFromImages([]image.Image{img}, magNearest)
}

func (ogl2 *OpenGL2Renderer) CreateTextureFromImages(pyramid []image.Image, magNearest bool) uint32 {
	var texid uint32
	gl.GenTextures(1, &texid)
	ogl2.UpdateTextureFromImages(texid, pyramid, magNearest)
	return texid
}

func (ogl2 *OpenGL2Renderer) UpdateTextureFromImage(texid uint32, img image.Image, magNearest bool) {
	ogl2.UpdateTextureFromImages(texid, []image.Image{img}, magNearest)
}

func (ogl2 *OpenGL2Renderer) UpdateTextureFromImages(texid uint32, pyramid []image.Image, magNearest bool) {
	var lastTexture int32
	gl.GetIntegerv(gl.TEXTURE_BINDING_2D, &lastTexture)

	gl.BindTexture(gl.TEXTURE_2D, texid)
	if len(pyramid) == 1 {
		gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
	} else {
		gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR_MIPMAP_LINEAR)
	}
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, int32(util.Select(magNearest, gl.NEAREST, gl.LINEAR)))
	gl.PixelStorei(gl.UNPACK_ROW_LENGTH, 0)

	bytes := 0
	for level, img := range pyramid {
		ny, nx := img.Bounds().Dy(), img.Bounds().Dx()
		bytes += 4 * nx * ny

		rgba, ok := img.(*image.RGBA)
		if !ok {
			rgba = image.NewRGBA(image.Rect(0, 0, nx, ny))
			draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
		}
		gl.TexImage2D(gl.TEXTURE_2D, int32(level), gl.RGBA, int32(nx), int32(ny), 0, gl.RGBA,
			gl.UNSIGNED_BYTE, unsafe.Pointer(&rgba.Pix[0]))
	}

	gl.BindTexture(gl.TEXTURE_2D, uint32(lastTexture))

	ogl2.createdTexture(texid, bytes)
}

func (ogl2 *OpenGL2Renderer) DestroyTexture(texid uint32) {
	gl.DeleteTextures(1, &texid)
	delete(ogl2.createdTextures, texid)
}

func (ogl2 *OpenGL2Renderer) RenderCommandBuffer(cb *CommandBuffer) RendererStats {
	var stats RendererStats
	stats.nBuffers++
	stats.bufferBytes += 4 * len(cb.Buf)

	i := 0
	ui32 := func() uint32 {
		v := cb.Buf[i]
		i++
		return v
	}
	i32 := func() int32 {
		return int32(ui32())
	}
	float := func() float32 {
		return gomath.Float32frombits(ui32())
	}

	for i < len(cb.Buf) {
		cmd := cb.Buf[i]
		i++
		switch cmd {
		case RendererLoadProjectionMatrix:
			gl.MatrixMode(gl.PROJECTION)
			ptr := (*float32)(unsafe.Pointer(&cb.Buf[i]))
			gl.LoadMatrixf(ptr)
			i += 16

		case RendererLoadModelViewMatrix:
			gl.MatrixMode(gl.MODELVIEW)
			ptr := (*float32)(unsafe.Pointer(&cb.Buf[i]))
			gl.LoadMatrixf(ptr)
			i += 16

		case RendererClearRGBA:
			r := float()
			g := float()
			b := float()
			a := float()
			gl.ClearColor(r, g, b, a)
			gl.Clear(gl.COLOR_BUFFER_BIT)

		case RendererScissor:
			x := i32()
			y := i32()
			w := i32()
			h := i32()
			gl.Enable(gl.SCISSOR_TEST)
			gl.Scissor(x, y, w, h)

		case RendererViewport:
			x := i32()
			y := i32()
			w := i32()
			h := i32()
			gl.Viewport(x, y, w, h)

		case RendererBlend:
			gl.Enable(gl.BLEND)
			gl.BlendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA)

		case RendererDisableBlend:
			gl.Disable(gl.BLEND)

		case RendererSetRGBA:
			r := float()
			g := float()
			b := float()
			a := float()
			gl.DisableClientState(gl.COLOR_ARRAY)
			gl.Color4f(r, g, b, a)

		case RendererFloatBuffer, RendererIntBuffer, RendererRawBuffer:
			// Nothing to do for the moment but skip ahead
			i += int(ui32())

		case RendererEnableTexture:
			gl.Enable(gl.TEXTURE_2D)
			gl.BindTexture(gl.TEXTURE_2D, ui32())

		case RendererDisableTexture:
			gl.Disable(gl.TEXTURE_2D)

		case RendererVertexArray:
			gl.EnableClientState(gl.VERTEX_ARRAY)
			offset := ui32()
			ptr := uintptr(unsafe.Pointer(&cb.Buf[0])) + uintptr(offset)
			nc := i32()
			stride := i32()
			gl.VertexPointer(nc, gl.FLOAT, stride, unsafe.Pointer(ptr))

		case RendererDisableVertexArray:
			gl.DisableClientState(gl.VERTEX_ARRAY)

		case RendererRGB32Array:
			gl.EnableClientState(gl.COLOR_ARRAY)
			offset := ui32()
			ptr := uintptr(unsafe.Pointer(&cb.Buf[0])) + uintptr(offset)
			nc := i32()
			stride := i32()
			gl.ColorPointer(nc, gl.FLOAT, stride, unsafe.Pointer(ptr))

		case RendererRGB8Array:
			gl.EnableClientState(gl.COLOR_ARRAY)
			offset := ui32()
			ptr := uintptr(unsafe.Pointer(&cb.Buf[0])) + uintptr(offset)
			nc := i32()
			stride := i32()
			gl.ColorPointer(nc, gl.UNSIGNED_BYTE, stride, unsafe.Pointer(ptr))

		case RendererDisableColorArray:
			gl.DisableClientState(gl.COLOR_ARRAY)

		case RendererTexCoordArray:
			gl.EnableClientState(gl.TEXTURE_COORD_ARRAY)
			offset := ui32()
			ptr := uintptr(unsafe.Pointer(&cb.Buf[0])) + uintptr(offset)
			nc := i32()
			stride := i32()
			gl.TexCoordPointer(nc, gl.FLOAT, stride, unsafe.Pointer(ptr))

		case RendererDisableTexCoordArray:
			gl.DisableClientState(gl.TEXTURE_COORD_ARRAY)

		case RendererLineWidth:
			gl.LineWidth(float())

		case RendererDrawLines:
			offset := ui32()
			ptr := uintptr(unsafe.Pointer(&cb.Buf[0])) + uintptr(offset)
			count := i32()
			gl.DrawElements(gl.LINES, count, gl.UNSIGNED_INT, unsafe.Pointer(ptr))

			stats.nDrawCalls++
			stats.nLines += int(count / 2)

		case RendererDrawTriangles:
			offset := ui32()
			ptr := uintptr(unsafe.Pointer(&cb.Buf[0])) + uintptr(offset)
			count := i32()
			gl.DrawElements(gl.TRIANGLES, count, gl.UNSIGNED_INT, unsafe.Pointer(ptr))

			stats.nDrawCalls++
			stats.nTriangles += int(count / 3)

		case RendererDrawQuads:
			offset := ui32()
			ptr := uintptr(unsafe.Pointer(&cb.Buf[0])) + uintptr(offset)
			count := i32()
			gl.DrawElements(gl.QUADS, count, gl.UNSIGNED_INT, unsafe.Pointer(ptr))

			stats.nDrawCalls++
			stats.nQuads += int(count / 4)

		case RendererResetState:
			gl.Disable(gl.SCISSOR_TEST)
			// viewport?
			gl.Disable(gl.BLEND)
			gl.DisableClientState(gl.VERTEX_ARRAY)
			gl.DisableClientState(gl.COLOR_ARRAY)
			gl.DisableClientState(gl.TEXTURE_COORD_ARRAY)
			gl.Disable(gl.TEXTURE_2D)
			gl.Disable(gl.POLYGON_STIPPLE)

		case RendererCallBuffer:
			idx := ui32()
			s2 := ogl2.RenderCommandBuffer(&cb.called[idx])
			stats.Merge(s2)

		case RendererEnablePolygonStipple:
			gl.Enable(gl.POLYGON_STIPPLE)

		case RendererDisablePolygonStipple:
			gl.Disable(gl.POLYGON_STIPPLE)

		case RendererPolygonStipple:
			mask := unsafe.Pointer(&cb.Buf[i])
			i += 32
			gl.PolygonStipple((*uint8)(mask))

		default:
			ogl2.lg.Error("unhandled command")
		}
	}

	return stats
}

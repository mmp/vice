// renderer/ogl21/ogl.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package ogl21

import (
	"C"
	"fmt"
	"image"
	"image/draw"
	gomath "math"
	"unsafe"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"

	"github.com/go-gl/gl/v2.1/gl"
)

type ogl struct {
	lg              *log.Logger
	createdTextures map[uint32]int
	vendor, device  string
}

// NewRenderer initializes OpenGL in the current context and returns a
// Renderer that draws into it.
func NewRenderer(lg *log.Logger) (renderer.Renderer, error) {
	lg.Info("Starting OpenGL 2.1 renderer initialization")
	if err := gl.Init(); err != nil {
		return nil, fmt.Errorf("failed to initialize OpenGL: %w", err)
	}
	vendor := C.GoString((*C.char)(unsafe.Pointer(gl.GetString(gl.VENDOR))))
	device := C.GoString((*C.char)(unsafe.Pointer(gl.GetString(gl.RENDERER))))
	lg.Infof("OpenGL vendor %s renderer %s", vendor, device)

	lg.Info("Finished OpenGL 2.1 renderer initialization")
	return &ogl{
		lg:              lg,
		createdTextures: make(map[uint32]int),
		vendor:          vendor,
		device:          device,
	}, nil
}

func (o *ogl) GetGPUInfo() (vendor, device string) {
	return o.vendor, o.device
}

func (o *ogl) Dispose() {
	for texid := range o.createdTextures {
		gl.DeleteTextures(1, &texid)
	}
}

func (o *ogl) createdTexture(texid uint32, bytes int) {
	_, exists := o.createdTextures[texid]

	o.createdTextures[texid] = bytes

	reduce := func(id uint32, bytes int, total int) int { return total + bytes }
	total := util.ReduceMap(o.createdTextures, reduce, 0)
	mb := float32(total) / (1024 * 1024)

	if exists {
		o.lg.Infof("Updated tex id %d: %d bytes -> %.2f MiB of textures total", texid, bytes, mb)
	} else {
		o.lg.Infof("Created tex id %d: %d bytes -> %.2f MiB of textures total", texid, bytes, mb)
	}
}

func (o *ogl) CreateTextureFromImage(img image.Image, magNearest bool) uint32 {
	return o.CreateTextureFromImages([]image.Image{img}, magNearest)
}

func (o *ogl) CreateTextureFromImages(pyramid []image.Image, magNearest bool) uint32 {
	var texid uint32
	gl.GenTextures(1, &texid)
	o.UpdateTextureFromImages(texid, pyramid, magNearest)
	return texid
}

func (o *ogl) UpdateTextureFromImage(texid uint32, img image.Image, magNearest bool) {
	o.UpdateTextureFromImages(texid, []image.Image{img}, magNearest)
}

func (o *ogl) UpdateTextureFromImages(texid uint32, pyramid []image.Image, magNearest bool) {
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

	o.createdTexture(texid, bytes)
}

func (o *ogl) DestroyTexture(texid uint32) {
	gl.DeleteTextures(1, &texid)
	delete(o.createdTextures, texid)
}

func (o *ogl) ReadPixelRGBAs(x, y, width, height int) []uint8 {
	pxf := make([]float32, 4*width*height)
	px := make([]uint8, 4*width*height)
	gl.Finish()
	gl.ReadPixels(int32(x), int32(y), int32(width), int32(height), gl.RGBA, gl.FLOAT, unsafe.Pointer(&pxf[0]))

	for i, v := range pxf {
		if 255*v > 255 {
			px[i] = 255
		} else {
			px[i] = uint8(255 * v)
		}
	}
	return px
}

func (o *ogl) RenderCommandBuffer(cb *renderer.CommandBuffer) renderer.Stats {
	// Unbind any VBOs/EBOs that may have been left by external renderers
	// (e.g. the imgui OGL3 backend). Our renderer uses client-side arrays;
	// if a buffer object is still bound, glDrawElements interprets its
	// pointer argument as a buffer offset instead of a memory address.
	gl.BindBuffer(gl.ARRAY_BUFFER, 0)
	gl.BindBuffer(gl.ELEMENT_ARRAY_BUFFER, 0)

	var stats renderer.Stats
	stats.NBuffers++
	stats.BufferBytes += 4 * len(cb.Buf)

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
	offsetPtr := func(offset uint32) unsafe.Pointer {
		return unsafe.Pointer(uintptr(unsafe.Pointer(&cb.Buf[0])) + uintptr(offset))
	}

	for i < len(cb.Buf) {
		cmd := cb.Buf[i]
		i++
		switch renderer.Command(cmd) {
		case renderer.CmdLoadProjectionMatrix:
			gl.MatrixMode(gl.PROJECTION)
			ptr := (*float32)(unsafe.Pointer(&cb.Buf[i]))
			gl.LoadMatrixf(ptr)
			i += 16

		case renderer.CmdLoadModelViewMatrix:
			gl.MatrixMode(gl.MODELVIEW)
			ptr := (*float32)(unsafe.Pointer(&cb.Buf[i]))
			gl.LoadMatrixf(ptr)
			i += 16

		case renderer.CmdClearRGBA:
			red := float()
			green := float()
			blue := float()
			alpha := float()
			gl.ClearColor(red, green, blue, alpha)
			gl.Clear(gl.COLOR_BUFFER_BIT)

		case renderer.CmdScissor:
			x := i32()
			y := i32()
			w := i32()
			h := i32()
			gl.Enable(gl.SCISSOR_TEST)
			gl.Scissor(x, y, w, h)

		case renderer.CmdViewport:
			x := i32()
			y := i32()
			w := i32()
			h := i32()
			gl.Viewport(x, y, w, h)

		case renderer.CmdBlend:
			gl.Enable(gl.BLEND)
			gl.BlendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA)

		case renderer.CmdDisableBlend:
			gl.Disable(gl.BLEND)

		case renderer.CmdSetRGBA:
			red := float()
			green := float()
			blue := float()
			alpha := float()
			gl.DisableClientState(gl.COLOR_ARRAY)
			gl.Color4f(red, green, blue, alpha)

		case renderer.CmdFloatBuffer, renderer.CmdIntBuffer, renderer.CmdRawBuffer:
			// Nothing to do for the moment but skip ahead
			i += int(ui32())

		case renderer.CmdEnableTexture:
			gl.Enable(gl.TEXTURE_2D)
			gl.BindTexture(gl.TEXTURE_2D, ui32())

		case renderer.CmdDisableTexture:
			gl.Disable(gl.TEXTURE_2D)

		case renderer.CmdVertexArray:
			gl.EnableClientState(gl.VERTEX_ARRAY)
			offset := ui32()
			ptr := offsetPtr(offset)
			nc := i32()
			stride := i32()
			gl.VertexPointer(nc, gl.FLOAT, stride, ptr)

		case renderer.CmdDisableVertexArray:
			gl.DisableClientState(gl.VERTEX_ARRAY)

		case renderer.CmdRGB32Array:
			gl.EnableClientState(gl.COLOR_ARRAY)
			offset := ui32()
			ptr := offsetPtr(offset)
			nc := i32()
			stride := i32()
			gl.ColorPointer(nc, gl.FLOAT, stride, ptr)

		case renderer.CmdRGB8Array:
			gl.EnableClientState(gl.COLOR_ARRAY)
			offset := ui32()
			ptr := offsetPtr(offset)
			nc := i32()
			stride := i32()
			gl.ColorPointer(nc, gl.UNSIGNED_BYTE, stride, ptr)

		case renderer.CmdDisableColorArray:
			gl.DisableClientState(gl.COLOR_ARRAY)

		case renderer.CmdTexCoordArray:
			gl.EnableClientState(gl.TEXTURE_COORD_ARRAY)
			offset := ui32()
			ptr := offsetPtr(offset)
			nc := i32()
			stride := i32()
			gl.TexCoordPointer(nc, gl.FLOAT, stride, ptr)

		case renderer.CmdDisableTexCoordArray:
			gl.DisableClientState(gl.TEXTURE_COORD_ARRAY)

		case renderer.CmdLineWidth:
			gl.LineWidth(float())

		case renderer.CmdDrawLines:
			offset := ui32()
			ptr := offsetPtr(offset)
			count := i32()
			gl.DrawElements(gl.LINES, count, gl.UNSIGNED_INT, ptr)

			stats.NDrawCalls++
			stats.NLines += int(count / 2)

		case renderer.CmdDrawTriangles:
			offset := ui32()
			ptr := offsetPtr(offset)
			count := i32()
			gl.DrawElements(gl.TRIANGLES, count, gl.UNSIGNED_INT, ptr)

			stats.NDrawCalls++
			stats.NTriangles += int(count / 3)

		case renderer.CmdDrawQuads:
			offset := ui32()
			ptr := offsetPtr(offset)
			count := i32()
			gl.DrawElements(gl.QUADS, count, gl.UNSIGNED_INT, ptr)

			stats.NDrawCalls++
			stats.NQuads += int(count / 4)

		case renderer.CmdResetState:
			gl.Disable(gl.SCISSOR_TEST)
			// viewport?
			gl.Disable(gl.BLEND)
			gl.DisableClientState(gl.VERTEX_ARRAY)
			gl.DisableClientState(gl.COLOR_ARRAY)
			gl.DisableClientState(gl.TEXTURE_COORD_ARRAY)
			gl.Disable(gl.TEXTURE_2D)
			gl.Disable(gl.POLYGON_STIPPLE)

		case renderer.CmdCallBuffer:
			idx := ui32()
			s2 := o.RenderCommandBuffer(&cb.Called[idx])
			stats.Merge(s2)

		case renderer.CmdEnablePolygonStipple:
			gl.Enable(gl.POLYGON_STIPPLE)

		case renderer.CmdDisablePolygonStipple:
			gl.Disable(gl.POLYGON_STIPPLE)

		case renderer.CmdPolygonStipple:
			mask := unsafe.Pointer(&cb.Buf[i])
			i += 32
			gl.PolygonStipple((*uint8)(mask))

		default:
			o.lg.Error("unhandled command")
		}
	}

	return stats
}

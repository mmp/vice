// ogl2renderer.go
//
// The OpenGL2Renderer class is  derived from the example renderer from
// inkyblackness/imgui-go-examples.

package main

import (
	"C"
	"fmt"
	"image"
	"math"
	"unsafe"

	"github.com/go-gl/gl/v2.1/gl"
	"github.com/mmp/imgui-go/v4"
)

// OpenGL2Renderer implements a renderer based on github.com/go-gl/gl
// (v2.1).
type OpenGL2Renderer struct {
	imguiIO imgui.IO

	fontTexture     uint32
	createdTextures []uint32
}

// NewOpenGL2Renderer creates an OpenGL context and creates a texture for the imgui fonts.
// Thus, all font creation must be finished before the renderer is created.
func NewOpenGL2Renderer(io imgui.IO) (Renderer, error) {
	lg.Printf("Starting OpenGL2Renderer initialization")
	if err := gl.Init(); err != nil {
		return nil, fmt.Errorf("failed to initialize OpenGL: %w", err)
	}
	vendor, renderer := gl.GetString(gl.VENDOR), gl.GetString(gl.RENDERER)
	v, r := (*C.char)(unsafe.Pointer(vendor)), (*C.char)(unsafe.Pointer(renderer))
	lg.Printf("OpenGL vendor %s renderer %s", C.GoString(v), C.GoString(r))

	ogl2 := &OpenGL2Renderer{imguiIO: io}
	lg.Printf("Creating fonts texture")
	ogl2.createFontsTexture()

	lg.Printf("Finished OpenGL2Renderer initialization")
	return ogl2, nil
}

func (ogl2 *OpenGL2Renderer) Dispose() {
	if ogl2.fontTexture != 0 {
		gl.DeleteTextures(1, &ogl2.fontTexture)
		imgui.CurrentIO().Fonts().SetTextureID(0)
		ogl2.fontTexture = 0
	}

	for _, texid := range ogl2.createdTextures {
		gl.DeleteTextures(1, &texid)
	}
}

func (ogl2 *OpenGL2Renderer) RenderImgui(displaySize [2]float32, framebufferSize [2]float32, drawData imgui.DrawData) {
	// Avoid rendering when minimized, scale coordinates for retina displays (screen coordinates != framebuffer coordinates)
	displayWidth, displayHeight := displaySize[0], displaySize[1]
	fbWidth, fbHeight := framebufferSize[0], framebufferSize[1]
	if (fbWidth <= 0) || (fbHeight <= 0) {
		return
	}
	drawData.ScaleClipRects(imgui.Vec2{
		X: fbWidth / displayWidth,
		Y: fbHeight / displayHeight,
	})

	// Setup render state: alpha-blending enabled, no face culling, no depth testing, scissor enabled, vertex/texcoord/color pointers, polygon fill.
	var lastTexture int32
	gl.GetIntegerv(gl.TEXTURE_BINDING_2D, &lastTexture)
	var lastPolygonMode [2]int32
	gl.GetIntegerv(gl.POLYGON_MODE, &lastPolygonMode[0])
	var lastViewport [4]int32
	gl.GetIntegerv(gl.VIEWPORT, &lastViewport[0])
	var lastScissorBox [4]int32
	gl.GetIntegerv(gl.SCISSOR_BOX, &lastScissorBox[0])
	gl.PushAttrib(gl.ENABLE_BIT | gl.COLOR_BUFFER_BIT | gl.TRANSFORM_BIT)
	gl.Enable(gl.BLEND)
	gl.BlendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA)
	gl.Disable(gl.CULL_FACE)
	gl.Disable(gl.DEPTH_TEST)
	gl.Disable(gl.LIGHTING)
	gl.Disable(gl.COLOR_MATERIAL)
	gl.Enable(gl.SCISSOR_TEST)
	gl.EnableClientState(gl.VERTEX_ARRAY)
	gl.EnableClientState(gl.TEXTURE_COORD_ARRAY)
	gl.EnableClientState(gl.COLOR_ARRAY)
	gl.Enable(gl.TEXTURE_2D)
	gl.PolygonMode(gl.FRONT_AND_BACK, gl.FILL)

	// You may want this if using this code in an OpenGL 3+ context where shaders may be bound
	// gl.UseProgram(0)

	// Setup viewport, orthographic projection matrix
	// Our visible imgui space lies from draw_data->DisplayPos (top left) to draw_data->DisplayPos+data_data->DisplaySize (bottom right).
	// DisplayMin is typically (0,0) for single viewport apps.
	gl.Viewport(0, 0, int32(fbWidth), int32(fbHeight))
	gl.MatrixMode(gl.PROJECTION)
	gl.PushMatrix()
	gl.LoadIdentity()
	gl.Ortho(0, float64(displayWidth), float64(displayHeight), 0, -1, 1)
	gl.MatrixMode(gl.MODELVIEW)
	gl.PushMatrix()
	gl.LoadIdentity()

	vertexSize, vertexOffsetPos, vertexOffsetUv, vertexOffsetCol := imgui.VertexBufferLayout()
	indexSize := imgui.IndexBufferLayout()

	drawType := gl.UNSIGNED_SHORT
	const bytesPerUint32 = 4
	if indexSize == bytesPerUint32 {
		drawType = gl.UNSIGNED_INT
	}

	// Render command lists
	for _, commandList := range drawData.CommandLists() {
		vertexBuffer, _ := commandList.VertexBuffer()
		indexBuffer, _ := commandList.IndexBuffer()
		indexBufferOffset := uintptr(indexBuffer)

		gl.VertexPointer(2, gl.FLOAT, int32(vertexSize), unsafe.Pointer(uintptr(vertexBuffer)+uintptr(vertexOffsetPos)))
		gl.TexCoordPointer(2, gl.FLOAT, int32(vertexSize), unsafe.Pointer(uintptr(vertexBuffer)+uintptr(vertexOffsetUv)))
		gl.ColorPointer(4, gl.UNSIGNED_BYTE, int32(vertexSize), unsafe.Pointer(uintptr(vertexBuffer)+uintptr(vertexOffsetCol)))

		for _, command := range commandList.Commands() {
			if command.HasUserCallback() {
				command.CallUserCallback(commandList)
			} else {
				clipRect := command.ClipRect()
				gl.Scissor(int32(clipRect.X), int32(fbHeight)-int32(clipRect.W), int32(clipRect.Z-clipRect.X), int32(clipRect.W-clipRect.Y))
				gl.BindTexture(gl.TEXTURE_2D, uint32(command.TextureID()))
				gl.DrawElementsWithOffset(gl.TRIANGLES, int32(command.ElementCount()), uint32(drawType), indexBufferOffset)
			}

			indexBufferOffset += uintptr(command.ElementCount() * indexSize)
		}
	}

	// Restore modified state
	gl.DisableClientState(gl.COLOR_ARRAY)
	gl.DisableClientState(gl.TEXTURE_COORD_ARRAY)
	gl.DisableClientState(gl.VERTEX_ARRAY)
	gl.BindTexture(gl.TEXTURE_2D, uint32(lastTexture))
	gl.MatrixMode(gl.MODELVIEW)
	gl.PopMatrix()
	gl.MatrixMode(gl.PROJECTION)
	gl.PopMatrix()
	gl.PopAttrib()
	gl.PolygonMode(gl.FRONT, uint32(lastPolygonMode[0]))
	gl.PolygonMode(gl.BACK, uint32(lastPolygonMode[1]))
	gl.Viewport(lastViewport[0], lastViewport[1], lastViewport[2], lastViewport[3])
	gl.Scissor(lastScissorBox[0], lastScissorBox[1], lastScissorBox[2], lastScissorBox[3])
}

func (ogl2 *OpenGL2Renderer) createFontsTexture() {
	// Build texture atlas
	image := ogl2.imguiIO.Fonts().TextureDataRGBA32()

	// Upload texture to graphics system
	var lastTexture int32
	gl.GetIntegerv(gl.TEXTURE_BINDING_2D, &lastTexture)
	gl.GenTextures(1, &ogl2.fontTexture)
	gl.BindTexture(gl.TEXTURE_2D, ogl2.fontTexture)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
	gl.PixelStorei(gl.UNPACK_ROW_LENGTH, 0)
	gl.TexImage2D(gl.TEXTURE_2D, 0, gl.RGBA, int32(image.Width), int32(image.Height), 0, gl.RGBA, gl.UNSIGNED_BYTE, image.Pixels)

	lg.Printf("Fonts texture used %.1f MB", float32(image.Width*image.Height*4)/(1024*1024))

	// Store our identifier
	ogl2.imguiIO.Fonts().SetTextureID(imgui.TextureID(ogl2.fontTexture))

	// Restore state
	gl.BindTexture(gl.TEXTURE_2D, uint32(lastTexture))
}

func (ogl2 *OpenGL2Renderer) CreateTextureFromImage(img image.Image, generateMIPs bool) (uint32, error) {
	ny, nx := img.Bounds().Dy(), img.Bounds().Dx()
	rgba := make([]byte, nx*ny*4)
	for y := 0; y < ny; y++ {
		for x := 0; x < nx; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			rgba[4*(nx*y+x)] = byte(r >> 8)
			rgba[4*(nx*y+x)+1] = byte(g >> 8)
			rgba[4*(nx*y+x)+2] = byte(b >> 8)
			rgba[4*(nx*y+x)+3] = byte(a >> 8)
		}
	}

	var lastTexture int32
	gl.GetIntegerv(gl.TEXTURE_BINDING_2D, &lastTexture)
	var texid uint32
	gl.GenTextures(1, &texid)
	gl.BindTexture(gl.TEXTURE_2D, texid)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
	gl.PixelStorei(gl.UNPACK_ROW_LENGTH, 0)
	gl.TexImage2D(gl.TEXTURE_2D, 0, gl.RGBA, int32(nx), int32(ny), 0, gl.RGBA, gl.UNSIGNED_BYTE, unsafe.Pointer(&rgba[0]))
	if generateMIPs {
		gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR_MIPMAP_LINEAR)
		level := int32(1)
		for nx != 1 || ny != 1 {
			ox, oy := nx, ny
			nx, ny = max(nx/2, 1), max(ny/2, 1)

			next := make([]byte, nx*ny*4)
			lookup := func(x, y, c int) int {
				x, y = min(x, ox-1), min(y, oy-1)
				return int(rgba[4*(ox*y+x)+c])
			}
			for y := 0; y < ny; y++ {
				for x := 0; x < nx; x++ {
					for c := 0; c < 4; c++ {
						// living large with a box filter
						v := (lookup(2*x, 2*y, c) + lookup(2*x+1, 2*y, c) +
							lookup(2*x, 2*y+1, c) + lookup(2*x+1, 2*y+1, c) + 2 /*rounding? */) / 4
						next[4*(nx*y+x)+c] = byte(v)
					}
				}
			}
			gl.TexImage2D(gl.TEXTURE_2D, level, gl.RGBA, int32(nx), int32(ny), 0, gl.RGBA, gl.UNSIGNED_BYTE,
				unsafe.Pointer(&next[0]))
			level++
			rgba = next
		}
	}

	gl.BindTexture(gl.TEXTURE_2D, uint32(lastTexture))

	ogl2.createdTextures = append(ogl2.createdTextures, texid)

	return texid, nil
}

func (ogl2 *OpenGL2Renderer) RenderCommandBuffer(cb *CommandBuffer) RendererStats {
	var stats RendererStats
	stats.nBuffers++
	stats.bufferBytes += 4 * len(cb.buf)

	i := 0
	ui32 := func() uint32 {
		v := cb.buf[i]
		i++
		return v
	}
	i32 := func() int32 {
		return int32(ui32())
	}
	float := func() float32 {
		return math.Float32frombits(ui32())
	}

	for i < len(cb.buf) {
		cmd := cb.buf[i]
		i++
		switch cmd {
		case RendererLoadProjectionMatrix:
			gl.MatrixMode(gl.PROJECTION)
			ptr := (*float32)(unsafe.Pointer(&cb.buf[i]))
			gl.LoadMatrixf(ptr)
			i += 16

		case RendererLoadModelViewMatrix:
			gl.MatrixMode(gl.MODELVIEW)
			ptr := (*float32)(unsafe.Pointer(&cb.buf[i]))
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

		case RendererFloatBuffer, RendererIntBuffer:
			// Nothing to do for the moment but skip ahead
			i += int(ui32())

		case RendererEnableTexture:
			gl.Enable(gl.TEXTURE_2D)
			gl.BindTexture(gl.TEXTURE_2D, ui32())

		case RendererDisableTexture:
			gl.Disable(gl.TEXTURE_2D)

		case RendererVertexArray:
			gl.EnableClientState(gl.VERTEX_ARRAY)
			ptr := &cb.buf[ui32()]
			nc := i32()
			stride := i32()
			gl.VertexPointer(nc, gl.FLOAT, stride, unsafe.Pointer(ptr))

		case RendererDisableVertexArray:
			gl.DisableClientState(gl.VERTEX_ARRAY)

		case RendererColorArray:
			gl.EnableClientState(gl.COLOR_ARRAY)
			ptr := &cb.buf[ui32()]
			nc := i32()
			stride := i32()
			gl.ColorPointer(nc, gl.FLOAT, stride, unsafe.Pointer(ptr))

		case RendererDisableColorArray:
			gl.DisableClientState(gl.COLOR_ARRAY)

		case RendererTexCoordArray:
			gl.EnableClientState(gl.TEXTURE_COORD_ARRAY)
			ptr := &cb.buf[ui32()]
			nc := i32()
			stride := i32()
			gl.TexCoordPointer(nc, gl.FLOAT, stride, unsafe.Pointer(ptr))

		case RendererDisableTexCoordArray:
			gl.DisableClientState(gl.TEXTURE_COORD_ARRAY)

		case RendererPointSize:
			gl.PointSize(float())

		case RendererDrawPoints:
			ptr := &cb.buf[ui32()]
			count := i32()

			gl.Enable(gl.ALPHA_TEST)
			gl.AlphaFunc(gl.NOTEQUAL, 0)
			gl.Enable(gl.BLEND)
			gl.BlendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA)
			gl.Enable(gl.POINT_SMOOTH)
			gl.Hint(gl.POINT_SMOOTH_HINT, gl.NICEST)

			gl.DrawElements(gl.POINTS, count, gl.UNSIGNED_INT, unsafe.Pointer(ptr))
			stats.nDrawCalls++
			stats.nPoints += int(count)

			gl.Disable(gl.POINT_SMOOTH)
			gl.BlendFunc(gl.NONE, gl.NONE)
			gl.Disable(gl.BLEND)

		case RendererLineWidth:
			gl.LineWidth(float())

		case RendererDrawLines:
			ptr := &cb.buf[ui32()]
			count := i32()
			gl.DrawElements(gl.LINES, count, gl.UNSIGNED_INT, unsafe.Pointer(ptr))

			stats.nDrawCalls++
			stats.nLines += int(count / 2)

		case RendererDrawTriangles:
			ptr := &cb.buf[ui32()]
			count := i32()
			gl.DrawElements(gl.TRIANGLES, count, gl.UNSIGNED_INT, unsafe.Pointer(ptr))

			stats.nDrawCalls++
			stats.nTriangles += int(count / 3)

		case RendererDrawQuads:
			ptr := &cb.buf[ui32()]
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

		case RendererCallBuffer:
			low := ui32()
			high := ui32()
			length := ui32()
			ptr := (*uint32)(unsafe.Pointer(uintptr((uint64(high) << 32) | uint64(low))))
			cb2 := CommandBuffer{buf: unsafe.Slice(ptr, length)}
			s2 := ogl2.RenderCommandBuffer(&cb2)
			stats.Merge(s2)

		default:
			lg.Errorf("unhandled command")
		}
	}

	return stats
}

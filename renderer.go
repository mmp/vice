// renderer.go
//
// The OpenGL2Renderer class is derived from the example renderer from
// inkyblackness/imgui-go-examples.

package main

import (
	"C"
	"fmt"
	"image/png"
	"io"
	"unsafe"

	"github.com/go-gl/gl/v2.1/gl"
	"github.com/mmp/imgui-go/v4"
)

// Renderer defines an interface for all of the various drawing that happens in vice.
// There is currently a single implementation of it--OpenGL2Renderer--though having
// all of these details behind the Renderer interface would make it realtively easy
// to write a Vulkan, Metal, or DirectX rendering backend.
type Renderer interface {
	// RenderImgui translates the ImGui draw data to OpenGL commands.
	RenderImgui(displaySize [2]float32, framebufferSize [2]float32, drawData imgui.DrawData)
	// RenderDrawList draws the draw commands encapsulated in drawList.
	RenderDrawList(drawList *DrawList)
	// RenderPaneSelectionQuad draws an alpha-blended transparent quad over
	// the specified region, for use in the pane editing UI. (TODO:
	// arguably, it should be possible to express this via a DrawList,
	// though it currently doesn't provide sufficient semantics for things
	// like blending.)
	RenderPaneSelectionQuad(scissor Extent2D, display Extent2D)
	// CreateTextureFromPNG returns an identifier for a texture map defined
	// by a PNG file from the provided Reader.
	CreateTextureFromPNG(r io.Reader) (uint32, error)
	// Dispose releases resources allocated by the renderer.
	Dispose()
}

// OpenGL2Renderer implements a renderer based on github.com/go-gl/gl (v2.1).
// Other than its RenderDrawList and RenderPaneSelectionQuad methods, it is
// based on the example OpenGL2 renderer in imgui-go-examples.
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

func (ogl2 *OpenGL2Renderer) CreateTextureFromPNG(r io.Reader) (uint32, error) {
	img, err := png.Decode(r)
	if err != nil {
		return 0, err
	}

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
	gl.BindTexture(gl.TEXTURE_2D, uint32(lastTexture))

	ogl2.createdTextures = append(ogl2.createdTextures, texid)

	return texid, nil
}

func (*OpenGL2Renderer) RenderDrawList(d *DrawList) {
	gl.Enable(gl.SCISSOR_TEST)
	gl.Viewport(int32(d.scissor.p0[0]), int32(d.scissor.p0[1]),
		int32(d.scissor.Width()), int32(d.scissor.Height()))
	gl.Scissor(int32(d.scissor.p0[0]), int32(d.scissor.p0[1]),
		int32(d.scissor.Width()), int32(d.scissor.Height()))

	// First clear
	if d.clear {
		gl.ClearColor(d.clearColor.R, d.clearColor.G, d.clearColor.B, 1)
		gl.Clear(gl.COLOR_BUFFER_BIT)
	}

	gl.EnableClientState(gl.VERTEX_ARRAY)
	gl.EnableClientState(gl.COLOR_ARRAY)

	gl.MatrixMode(gl.PROJECTION)
	gl.PushMatrix()
	gl.LoadMatrixf(&d.projection[0])
	gl.MatrixMode(gl.MODELVIEW)
	gl.PushMatrix()
	gl.LoadMatrixf(&d.modelview[0])

	if err := gl.GetError(); err != 0 {
		lg.Printf("GL Error %d %x", err, err)
	}

	// Draw triangles first
	for _, td := range d.tris {
		gl.VertexPointer(2, gl.FLOAT, 2*4, unsafe.Pointer(&td.p[0]))
		gl.ColorPointer(3, gl.FLOAT, 3*4, unsafe.Pointer(&td.color[0]))
		gl.DrawElements(gl.TRIANGLES, int32(len(td.indices)), gl.UNSIGNED_INT,
			unsafe.Pointer(&td.indices[0]))
	}

	// Now points
	gl.Enable(gl.ALPHA_TEST)
	gl.AlphaFunc(gl.NOTEQUAL, 0)
	gl.Enable(gl.BLEND)
	gl.BlendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA)
	gl.Enable(gl.POINT_SMOOTH)
	gl.Hint(gl.POINT_SMOOTH_HINT, gl.NICEST)
	for _, pd := range d.points {
		if pd.size > 0 {
			gl.PointSize(pd.size)
		} else {
			gl.PointSize(1)
		}
		gl.VertexPointer(2, gl.FLOAT, 2*4, unsafe.Pointer(&pd.p[0]))
		gl.ColorPointer(3, gl.FLOAT, 3*4, unsafe.Pointer(&pd.color[0]))
		gl.DrawElements(gl.POINTS, int32(len(pd.indices)), gl.UNSIGNED_INT,
			unsafe.Pointer(&pd.indices[0]))
	}
	gl.Disable(gl.POINT_SMOOTH)
	gl.BlendFunc(gl.NONE, gl.NONE)
	gl.Disable(gl.BLEND)

	// Now lines
	for _, ld := range d.lines {
		if ld.width > 0 {
			gl.LineWidth(ld.width)
		} else {
			gl.LineWidth(2)
		}
		gl.VertexPointer(2, gl.FLOAT, 2*4, unsafe.Pointer(&ld.p[0]))
		gl.ColorPointer(3, gl.FLOAT, 3*4, unsafe.Pointer(&ld.color[0]))
		gl.DrawElements(gl.LINES, int32(len(ld.indices)), gl.UNSIGNED_INT,
			unsafe.Pointer(&ld.indices[0]))
	}

	// Text...
	gl.Enable(gl.BLEND)
	gl.BlendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA)
	gl.Disable(gl.CULL_FACE)
	gl.Disable(gl.DEPTH_TEST)
	gl.Disable(gl.LIGHTING)
	gl.Disable(gl.COLOR_MATERIAL)
	gl.PolygonMode(gl.FRONT_AND_BACK, gl.FILL)

	// Draw any needed background quads while the vertex and color arrays
	// are still enabled.
	for _, td := range d.text {
		if len(td.bgindices) > 0 {
			gl.VertexPointer(2, gl.FLOAT, 2*4, unsafe.Pointer(&td.bgp[0]))
			gl.ColorPointer(3, gl.FLOAT, 3*4, unsafe.Pointer(&td.bgrgb[0]))
			gl.DrawElements(gl.QUADS, int32(len(td.bgindices)), gl.UNSIGNED_INT, unsafe.Pointer(&td.bgindices[0]))
		}
	}

	// And now set up state for texturing
	gl.EnableClientState(gl.TEXTURE_COORD_ARRAY)
	gl.Enable(gl.TEXTURE_2D)
	gl.BindTexture(gl.TEXTURE_2D, uint32(imgui.CurrentIO().Fonts().GetTextureID()))

	for _, td := range d.text {
		if len(td.indices) > 0 {
			gl.VertexPointer(2, gl.FLOAT, 2*4, unsafe.Pointer(&td.p[0]))
			gl.ColorPointer(3, gl.FLOAT, 3*4, unsafe.Pointer(&td.rgb[0]))
			gl.TexCoordPointer(2, gl.FLOAT, 2*4, unsafe.Pointer(&td.uv[0]))
			gl.DrawElements(gl.QUADS, int32(len(td.indices)), gl.UNSIGNED_INT, unsafe.Pointer(&td.indices[0]))
		}
	}

	gl.DisableClientState(gl.COLOR_ARRAY)
	gl.DisableClientState(gl.TEXTURE_COORD_ARRAY)
	gl.DisableClientState(gl.VERTEX_ARRAY)

	gl.Disable(gl.BLEND)
	gl.Disable(gl.TEXTURE_2D)

	gl.MatrixMode(gl.PROJECTION)
	gl.PopMatrix()
	gl.MatrixMode(gl.MODELVIEW)
	gl.PopMatrix()

	gl.Disable(gl.SCISSOR_TEST)
}

func (*OpenGL2Renderer) RenderPaneSelectionQuad(scissor Extent2D, display Extent2D) {
	gl.Enable(gl.SCISSOR_TEST)
	gl.Viewport(int32(scissor.p0[0]), int32(scissor.p0[1]),
		int32(scissor.Width()), int32(scissor.Height()))
	gl.Scissor(int32(scissor.p0[0]), int32(scissor.p0[1]),
		int32(scissor.Width()), int32(scissor.Height()))

	gl.MatrixMode(gl.PROJECTION)
	gl.PushMatrix()
	gl.LoadIdentity()
	gl.Ortho(0, float64(display.Width()), 0, float64(display.Height()), -1, 1)
	gl.MatrixMode(gl.MODELVIEW)
	gl.PushMatrix()
	gl.LoadIdentity()

	gl.Enable(gl.BLEND)
	gl.BlendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA)
	gl.Color4f(0.5, 0.5, 0.5, 0.5)
	gl.Begin(gl.QUADS)
	gl.Vertex2f(0, 0)
	gl.Vertex2f(display.Width(), 0)
	gl.Vertex2f(display.Width(), display.Height())
	gl.Vertex2f(0, display.Height())
	gl.End()
	gl.Disable(gl.BLEND)

	gl.PopMatrix()
	gl.MatrixMode(gl.PROJECTION)
	gl.PopMatrix()

	gl.Disable(gl.SCISSOR_TEST)
}

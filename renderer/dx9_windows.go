// pkg/renderer/dx9_windows.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

//go:build windows

package renderer

import (
	"fmt"
	"image"
	"image/draw"
	gomath "math"
	"unsafe"

	"github.com/gonutz/d3d9"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/util"
)

// Also available as a global, though only used by CommandBuffer
var lg *log.Logger

// DirectX9Renderer implements the Renderer interface using DirectX 9.
type DirectX9Renderer struct {
	lg              *log.Logger
	d3d             *d3d9.Direct3D
	device          *d3d9.Device
	hwnd            uintptr
	createdTextures map[uint32]*textureInfo

	// For VSync control
	vsyncEnabled bool

	// Stipple texture for polygon stipple emulation
	stippleTexture uint32
}

// arrayState tracks the currently enabled vertex arrays
type arrayState struct {
	// Vertex position array
	vertexEnabled bool
	vertexOffset  uint32
	vertexComps   int32
	vertexStride  int32

	// Color array (RGB32 float or RGB8 byte)
	colorEnabled bool
	colorOffset  uint32
	colorComps   int32
	colorStride  int32
	colorIsFloat bool // true for RGB32, false for RGB8

	// Texture coordinate array
	texCoordEnabled bool
	texCoordOffset  uint32
	texCoordComps   int32
	texCoordStride  int32
}

type textureInfo struct {
	texture *d3d9.Texture
	bytes   int
}

// Vertex format for our rendering
// Position (2 floats) + Color (4 bytes BGRA) + TexCoord (2 floats)
const (
	d3dfvfXYZ    = 0x002
	d3dfvfXYZRHW = 0x004 // Transformed vertices (screen space)
	d3dfvfTex1   = 0x100
	d3dfvfTex0   = 0x000

	// Custom FVF: transformed position + diffuse color + 1 texture coord
	customFVF = d3dfvfXYZRHW | d3d9.FVF_DIFFUSE | d3dfvfTex1
)

// NewDirectX9Renderer creates a DirectX 9 renderer.
func NewDirectX9Renderer(hwnd uintptr, l *log.Logger) (Renderer, error) {
	lg = l

	lg.Info("Starting DirectX9Renderer initialization")

	d3d, err := d3d9.Create(d3d9.SDK_VERSION)
	if err != nil {
		return nil, fmt.Errorf("failed to create Direct3D9: %w", err)
	}

	// Get adapter info
	adapterID, err := d3d.GetAdapterIdentifier(d3d9.ADAPTER_DEFAULT, 0)
	if err == nil {
		lg.Infof("DirectX9 adapter: %s", adapterID.Description)
	}

	// Create device
	pp := d3d9.PRESENT_PARAMETERS{
		Windowed:               1,
		SwapEffect:             d3d9.SWAPEFFECT_DISCARD,
		BackBufferFormat:       d3d9.FMT_UNKNOWN, // Use current display format
		EnableAutoDepthStencil: 0,
		PresentationInterval:   d3d9.PRESENT_INTERVAL_ONE, // VSync on
	}

	device, _, err := d3d.CreateDevice(
		d3d9.ADAPTER_DEFAULT,
		d3d9.DEVTYPE_HAL,
		d3d9.HWND(hwnd),
		d3d9.CREATE_SOFTWARE_VERTEXPROCESSING,
		pp,
	)
	if err != nil {
		d3d.Release()
		return nil, fmt.Errorf("failed to create D3D9 device: %w", err)
	}

	// Set up default render states
	device.SetRenderState(d3d9.RS_CULLMODE, d3d9.CULL_NONE)
	device.SetRenderState(d3d9.RS_LIGHTING, 0)
	device.SetRenderState(d3d9.RS_ZENABLE, 0)
	device.SetRenderState(d3d9.RS_ZWRITEENABLE, 0)

	// Set up initial texture stage state - no texture bound, use vertex color only
	device.SetTextureStageState(0, d3d9.TSS_COLOROP, d3d9.TOP_SELECTARG1)
	device.SetTextureStageState(0, d3d9.TSS_COLORARG1, d3d9.TA_DIFFUSE)
	device.SetTextureStageState(0, d3d9.TSS_ALPHAOP, d3d9.TOP_SELECTARG1)
	device.SetTextureStageState(0, d3d9.TSS_ALPHAARG1, d3d9.TA_DIFFUSE)

	// Set sampler states
	device.SetSamplerState(0, d3d9.SAMP_MINFILTER, d3d9.TEXF_LINEAR)
	device.SetSamplerState(0, d3d9.SAMP_MAGFILTER, d3d9.TEXF_LINEAR)

	lg.Info("Finished DirectX9Renderer initialization")

	return &DirectX9Renderer{
		lg:              lg,
		d3d:             d3d,
		device:          device,
		hwnd:            hwnd,
		createdTextures: make(map[uint32]*textureInfo),
		vsyncEnabled:    true,
	}, nil
}

func (r *DirectX9Renderer) Dispose() {
	// Release all textures
	for texid, info := range r.createdTextures {
		if info.texture != nil {
			info.texture.Release()
		}
		delete(r.createdTextures, texid)
	}

	if r.device != nil {
		r.device.Release()
	}
	if r.d3d != nil {
		r.d3d.Release()
	}
}

func (r *DirectX9Renderer) createdTexture(texid uint32, bytes int) {
	_, exists := r.createdTextures[texid]

	if r.createdTextures[texid] != nil {
		r.createdTextures[texid].bytes = bytes
	}

	reduce := func(id uint32, info *textureInfo, total int) int { return total + info.bytes }
	total := util.ReduceMap[uint32, *textureInfo, int](r.createdTextures, reduce, 0)
	mb := float32(total) / (1024 * 1024)

	if exists {
		r.lg.Infof("Updated tex id %d: %d bytes -> %.2f MiB of textures total", texid, bytes, mb)
	} else {
		r.lg.Infof("Created tex id %d: %d bytes -> %.2f MiB of textures total", texid, bytes, mb)
	}
}

var nextTextureID uint32 = 1

func (r *DirectX9Renderer) CreateTextureFromImage(img image.Image, magNearest bool) uint32 {
	return r.CreateTextureFromImages([]image.Image{img}, magNearest)
}

func (r *DirectX9Renderer) CreateTextureFromImages(pyramid []image.Image, magNearest bool) uint32 {
	texid := nextTextureID
	nextTextureID++

	r.createdTextures[texid] = &textureInfo{}
	r.UpdateTextureFromImages(texid, pyramid, magNearest)
	return texid
}

func (r *DirectX9Renderer) UpdateTextureFromImage(texid uint32, img image.Image, magNearest bool) {
	r.UpdateTextureFromImages(texid, []image.Image{img}, magNearest)
}

func (r *DirectX9Renderer) UpdateTextureFromImages(texid uint32, pyramid []image.Image, magNearest bool) {
	info := r.createdTextures[texid]
	if info == nil {
		return
	}

	// Release old texture if exists
	if info.texture != nil {
		info.texture.Release()
		info.texture = nil
	}

	img := pyramid[0]
	ny, nx := img.Bounds().Dy(), img.Bounds().Dx()

	// Create new texture
	tex, err := r.device.CreateTexture(
		uint(nx), uint(ny),
		uint(len(pyramid)), // Mip levels
		0,                  // Usage
		d3d9.FMT_A8R8G8B8,  // BGRA format
		d3d9.POOL_MANAGED,
		0,
	)
	if err != nil {
		r.lg.Errorf("Failed to create texture: %v", err)
		return
	}

	bytes := 0
	for level, img := range pyramid {
		ny, nx := img.Bounds().Dy(), img.Bounds().Dx()
		bytes += 4 * nx * ny

		// Convert to RGBA
		rgba, ok := img.(*image.RGBA)
		if !ok {
			rgba = image.NewRGBA(image.Rect(0, 0, nx, ny))
			draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
		}

		// Lock the texture
		rect, err := tex.LockRect(uint(level), nil, d3d9.LOCK_DISCARD)
		if err != nil {
			r.lg.Errorf("Failed to lock texture: %v", err)
			tex.Release()
			return
		}

		// Copy data, converting RGBA to BGRA
		// Create a buffer with the converted data
		srcPitch := nx * 4
		dstData := make([]byte, ny*srcPitch)
		for y := 0; y < ny; y++ {
			for x := 0; x < nx; x++ {
				srcIdx := y*srcPitch + x*4
				dstIdx := y*srcPitch + x*4

				// Convert RGBA to BGRA
				dstData[dstIdx] = rgba.Pix[srcIdx+2]   // B
				dstData[dstIdx+1] = rgba.Pix[srcIdx+1] // G
				dstData[dstIdx+2] = rgba.Pix[srcIdx]   // R
				dstData[dstIdx+3] = rgba.Pix[srcIdx+3] // A
			}
		}
		rect.SetAllBytes(dstData, srcPitch)

		tex.UnlockRect(uint(level))
	}

	info.texture = tex

	// Set sampler state for mag filter
	if magNearest {
		r.device.SetSamplerState(0, d3d9.SAMP_MAGFILTER, d3d9.TEXF_POINT)
	} else {
		r.device.SetSamplerState(0, d3d9.SAMP_MAGFILTER, d3d9.TEXF_LINEAR)
	}

	// Set mip filter
	if len(pyramid) > 1 {
		r.device.SetSamplerState(0, d3d9.SAMP_MIPFILTER, d3d9.TEXF_LINEAR)
	} else {
		r.device.SetSamplerState(0, d3d9.SAMP_MIPFILTER, d3d9.TEXF_NONE)
	}

	r.createdTexture(texid, bytes)
}

func (r *DirectX9Renderer) DestroyTexture(texid uint32) {
	info := r.createdTextures[texid]
	if info != nil && info.texture != nil {
		info.texture.Release()
	}
	delete(r.createdTextures, texid)
}

func (r *DirectX9Renderer) ReadPixelRGBAs(x, y, width, height int) []uint8 {
	// Get backbuffer
	backbuffer, err := r.device.GetBackBuffer(0, 0, d3d9.BACKBUFFER_TYPE_MONO)
	if err != nil {
		r.lg.Errorf("Failed to get backbuffer: %v", err)
		return nil
	}
	defer backbuffer.Release()

	// Get surface description
	desc, err := backbuffer.GetDesc()
	if err != nil {
		r.lg.Errorf("Failed to get backbuffer desc: %v", err)
		return nil
	}

	// Create offscreen surface for readback
	surface, err := r.device.CreateOffscreenPlainSurface(
		uint(desc.Width), uint(desc.Height),
		desc.Format,
		d3d9.POOL_SYSTEMMEM,
		0,
	)
	if err != nil {
		r.lg.Errorf("Failed to create offscreen surface: %v", err)
		return nil
	}
	defer surface.Release()

	// Copy backbuffer to offscreen surface
	err = r.device.GetRenderTargetData(backbuffer, surface)
	if err != nil {
		r.lg.Errorf("Failed to get render target data: %v", err)
		return nil
	}

	// Lock surface and read pixels
	rect := &d3d9.RECT{
		Left:   int32(x),
		Top:    int32(y),
		Right:  int32(x + width),
		Bottom: int32(y + height),
	}
	lockedRect, err := surface.LockRect(rect, d3d9.LOCK_READONLY)
	if err != nil {
		r.lg.Errorf("Failed to lock surface: %v", err)
		return nil
	}

	// Note: LOCKED_RECT doesn't have a Bytes() method for reading
	// We'll need to use unsafe pointer arithmetic to read the data
	px := make([]uint8, 4*width*height)

	// Get pointer to the locked data
	dataPtr := lockedRect.PBits
	for row := 0; row < height; row++ {
		rowPtr := unsafe.Pointer(uintptr(dataPtr) + uintptr(row)*uintptr(lockedRect.Pitch))
		for col := 0; col < width; col++ {
			srcPtr := unsafe.Pointer(uintptr(rowPtr) + uintptr(col*4))
			src := (*[4]uint8)(srcPtr)
			dstIdx := (row*width + col) * 4

			// Read BGRA, write RGBA
			px[dstIdx] = src[2]   // R
			px[dstIdx+1] = src[1] // G
			px[dstIdx+2] = src[0] // B
			px[dstIdx+3] = src[3] // A
		}
	}

	surface.UnlockRect()

	return px
}

func (r *DirectX9Renderer) RenderCommandBuffer(cb *CommandBuffer) RendererStats {
	var stats RendererStats
	stats.nBuffers++
	stats.bufferBytes += 4 * len(cb.Buf)

	// Begin scene
	r.device.BeginScene()

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

	// Track current state
	var currentColor [4]float32 = [4]float32{1, 1, 1, 1}
	var projMatrix [16]float32
	var viewMatrix [16]float32
	var arrays arrayState
	var bufferBase unsafe.Pointer
	if len(cb.Buf) > 0 {
		bufferBase = unsafe.Pointer(&cb.Buf[0])
	}

	offsetPtr := func(offset uint32) unsafe.Pointer {
		return unsafe.Pointer(uintptr(bufferBase) + uintptr(offset))
	}

	for i < len(cb.Buf) {
		cmd := cb.Buf[i]
		i++

		switch cmd {
		case RendererLoadProjectionMatrix:
			// Read 16 floats
			for j := 0; j < 16; j++ {
				projMatrix[j] = float()
			}
			// Convert from OpenGL column-major to D3D9 row-major
			// d3d9.MATRIX is [16]float32 in row-major order
			var d3dMatrix d3d9.MATRIX
			for row := 0; row < 4; row++ {
				for col := 0; col < 4; col++ {
					d3dMatrix[row*4+col] = projMatrix[col*4+row]
				}
			}
			r.device.SetTransform(d3d9.TS_PROJECTION, d3dMatrix)

		case RendererLoadModelViewMatrix:
			// Read 16 floats
			for j := 0; j < 16; j++ {
				viewMatrix[j] = float()
			}
			// Convert from OpenGL column-major to D3D9 row-major
			var d3dMatrix d3d9.MATRIX
			for row := 0; row < 4; row++ {
				for col := 0; col < 4; col++ {
					d3dMatrix[row*4+col] = viewMatrix[col*4+row]
				}
			}
			// Set as world transform (D3D9 separates world and view)
			r.device.SetTransform(d3d9.TSWorldMatrix(0), d3dMatrix)
			// Set identity view matrix
			var identity d3d9.MATRIX
			identity[0] = 1  // [0][0]
			identity[5] = 1  // [1][1]
			identity[10] = 1 // [2][2]
			identity[15] = 1 // [3][3]
			r.device.SetTransform(d3d9.TS_VIEW, identity)

		case RendererClearRGBA:
			red := float()
			green := float()
			blue := float()
			alpha := float()
			// Convert to D3D color
			color := d3d9.ColorValue(red, green, blue, alpha)
			r.device.Clear(nil, d3d9.CLEAR_TARGET, color, 1.0, 0)

		case RendererScissor:
			x := i32()
			y := i32()
			w := i32()
			h := i32()
			r.device.SetRenderState(d3d9.RS_SCISSORTESTENABLE, 1)
			rect := d3d9.RECT{
				Left:   x,
				Top:    y,
				Right:  x + w,
				Bottom: y + h,
			}
			r.device.SetScissorRect(rect)

		case RendererViewport:
			x := i32()
			y := i32()
			w := i32()
			h := i32()
			vp := d3d9.VIEWPORT{
				X:      uint32(x),
				Y:      uint32(y),
				Width:  uint32(w),
				Height: uint32(h),
				MinZ:   0,
				MaxZ:   1,
			}
			r.device.SetViewport(vp)

		case RendererBlend:
			r.device.SetRenderState(d3d9.RS_ALPHABLENDENABLE, 1)
			r.device.SetRenderState(d3d9.RS_SRCBLEND, d3d9.BLEND_SRCALPHA)
			r.device.SetRenderState(d3d9.RS_DESTBLEND, d3d9.BLEND_INVSRCALPHA)

		case RendererDisableBlend:
			r.device.SetRenderState(d3d9.RS_ALPHABLENDENABLE, 0)

		case RendererSetRGBA:
			currentColor[0] = float()
			currentColor[1] = float()
			currentColor[2] = float()
			currentColor[3] = float()

		case RendererFloatBuffer, RendererIntBuffer, RendererRawBuffer:
			// Skip the buffer data
			i += int(ui32())

		case RendererEnableTexture:
			texid := ui32()
			info := r.createdTextures[texid]
			if info != nil && info.texture != nil {
				r.device.SetTexture(0, info.texture)
				// Configure texture stage to modulate texture with vertex color
				r.device.SetTextureStageState(0, d3d9.TSS_COLOROP, d3d9.TOP_MODULATE)
				r.device.SetTextureStageState(0, d3d9.TSS_COLORARG1, d3d9.TA_TEXTURE)
				r.device.SetTextureStageState(0, d3d9.TSS_COLORARG2, d3d9.TA_DIFFUSE)
				r.device.SetTextureStageState(0, d3d9.TSS_ALPHAOP, d3d9.TOP_MODULATE)
				r.device.SetTextureStageState(0, d3d9.TSS_ALPHAARG1, d3d9.TA_TEXTURE)
				r.device.SetTextureStageState(0, d3d9.TSS_ALPHAARG2, d3d9.TA_DIFFUSE)
			}

		case RendererDisableTexture:
			r.device.SetTexture(0, nil)
			// Configure texture stage to use only vertex color (no texture sampling)
			r.device.SetTextureStageState(0, d3d9.TSS_COLOROP, d3d9.TOP_SELECTARG1)
			r.device.SetTextureStageState(0, d3d9.TSS_COLORARG1, d3d9.TA_DIFFUSE)
			r.device.SetTextureStageState(0, d3d9.TSS_ALPHAOP, d3d9.TOP_SELECTARG1)
			r.device.SetTextureStageState(0, d3d9.TSS_ALPHAARG1, d3d9.TA_DIFFUSE)

		case RendererVertexArray:
			arrays.vertexEnabled = true
			arrays.vertexOffset = ui32()
			arrays.vertexComps = i32()
			arrays.vertexStride = i32()

		case RendererDisableVertexArray:
			arrays.vertexEnabled = false

		case RendererRGB32Array:
			arrays.colorEnabled = true
			arrays.colorOffset = ui32()
			arrays.colorComps = i32()
			arrays.colorStride = i32()
			arrays.colorIsFloat = true

		case RendererRGB8Array:
			arrays.colorEnabled = true
			arrays.colorOffset = ui32()
			arrays.colorComps = i32()
			arrays.colorStride = i32()
			arrays.colorIsFloat = false

		case RendererDisableColorArray:
			arrays.colorEnabled = false

		case RendererTexCoordArray:
			arrays.texCoordEnabled = true
			arrays.texCoordOffset = ui32()
			arrays.texCoordComps = i32()
			arrays.texCoordStride = i32()

		case RendererDisableTexCoordArray:
			arrays.texCoordEnabled = false

		case RendererLineWidth:
			// D3D9 doesn't support variable line width
			_ = float()

		case RendererDrawLines:
			offset := ui32()
			count := i32()
			// Get indices from buffer
			indices := (*[1 << 20]int32)(offsetPtr(offset))[:count:count]
			r.drawLines(bufferBase, indices, currentColor, &arrays)
			stats.nDrawCalls++
			stats.nLines += int(count / 2)

		case RendererDrawTriangles:
			offset := ui32()
			count := i32()
			indices := (*[1 << 20]int32)(offsetPtr(offset))[:count:count]
			r.drawTriangles(bufferBase, indices, currentColor, &arrays)
			stats.nDrawCalls++
			stats.nTriangles += int(count / 3)

		case RendererDrawQuads:
			offset := ui32()
			count := i32()
			indices := (*[1 << 20]int32)(offsetPtr(offset))[:count:count]
			r.drawQuads(bufferBase, indices, currentColor, &arrays)
			stats.nDrawCalls++
			stats.nQuads += int(count / 4)

		case RendererResetState:
			r.device.SetRenderState(d3d9.RS_SCISSORTESTENABLE, 0)
			r.device.SetRenderState(d3d9.RS_ALPHABLENDENABLE, 0)
			r.device.SetTexture(0, nil)

		case RendererCallBuffer:
			idx := ui32()
			s2 := r.RenderCommandBuffer(&cb.called[idx])
			stats.Merge(s2)

		case RendererEnablePolygonStipple:
			// Polygon stipple emulation - could use alpha texture
			// For now, just enable alpha test
			r.device.SetRenderState(d3d9.RS_ALPHATESTENABLE, 1)
			r.device.SetRenderState(d3d9.RS_ALPHAREF, 128)
			r.device.SetRenderState(d3d9.RS_ALPHAFUNC, d3d9.CMP_GREATEREQUAL)

		case RendererDisablePolygonStipple:
			r.device.SetRenderState(d3d9.RS_ALPHATESTENABLE, 0)

		case RendererPolygonStipple:
			// Skip the stipple pattern (32 uint32s)
			i += 32

		default:
			r.lg.Errorf("unhandled command: %d", cmd)
		}
	}

	// End scene
	r.device.EndScene()

	// Present
	r.device.Present(nil, nil, 0, nil)

	return stats
}

// vertex represents an interleaved vertex for D3D9 rendering
// Using XYZ (not XYZRHW) so that projection/modelview matrices are applied
type vertex struct {
	X, Y, Z float32
	Color   uint32
	U, V    float32
}

const vertexFVF = d3d9.FVF_XYZ | d3d9.FVF_DIFFUSE | d3d9.FVF_TEX1

// buildVertices extracts vertex data from the command buffer arrays and builds
// interleaved vertices for D3D9
func (r *DirectX9Renderer) buildVertices(bufferBase unsafe.Pointer, indices []int32, defaultColor [4]float32, arrays *arrayState) []vertex {
	vertices := make([]vertex, len(indices))
	d3dDefaultColor := d3dColorFromFloat(defaultColor)

	for i, idx := range indices {
		v := &vertices[i]
		v.Z = 0
		v.Color = d3dDefaultColor

		// Get position
		if arrays.vertexEnabled {
			posPtr := unsafe.Pointer(uintptr(bufferBase) + uintptr(arrays.vertexOffset) + uintptr(idx)*uintptr(arrays.vertexStride))
			pos := (*[2]float32)(posPtr)
			v.X = pos[0]
			v.Y = pos[1]
		}

		// Get color
		if arrays.colorEnabled {
			colorPtr := unsafe.Pointer(uintptr(bufferBase) + uintptr(arrays.colorOffset) + uintptr(idx)*uintptr(arrays.colorStride))
			if arrays.colorIsFloat {
				// RGB32 - 3 floats
				rgb := (*[3]float32)(colorPtr)
				v.Color = d3dColorFromFloat([4]float32{rgb[0], rgb[1], rgb[2], defaultColor[3]})
			} else {
				// RGB8 - 3 bytes
				rgb := (*[3]uint8)(colorPtr)
				a := uint32(defaultColor[3] * 255)
				v.Color = (a << 24) | (uint32(rgb[0]) << 16) | (uint32(rgb[1]) << 8) | uint32(rgb[2])
			}
		}

		// Get texture coordinates
		if arrays.texCoordEnabled {
			uvPtr := unsafe.Pointer(uintptr(bufferBase) + uintptr(arrays.texCoordOffset) + uintptr(idx)*uintptr(arrays.texCoordStride))
			uv := (*[2]float32)(uvPtr)
			v.U = uv[0]
			v.V = uv[1]
		}
	}

	return vertices
}

// drawLines renders lines using DrawPrimitiveUP
func (r *DirectX9Renderer) drawLines(bufferBase unsafe.Pointer, indices []int32, color [4]float32, arrays *arrayState) {
	if len(indices) < 2 {
		return
	}

	vertices := r.buildVertices(bufferBase, indices, color, arrays)

	r.device.SetFVF(vertexFVF)
	r.device.DrawPrimitiveUP(
		d3d9.PT_LINELIST,
		uint(len(indices)/2),
		uintptr(unsafe.Pointer(&vertices[0])),
		uint(unsafe.Sizeof(vertex{})),
	)
}

// drawTriangles renders triangles using DrawPrimitiveUP
func (r *DirectX9Renderer) drawTriangles(bufferBase unsafe.Pointer, indices []int32, color [4]float32, arrays *arrayState) {
	if len(indices) < 3 {
		return
	}

	vertices := r.buildVertices(bufferBase, indices, color, arrays)

	r.device.SetFVF(vertexFVF)
	r.device.DrawPrimitiveUP(
		d3d9.PT_TRIANGLELIST,
		uint(len(indices)/3),
		uintptr(unsafe.Pointer(&vertices[0])),
		uint(unsafe.Sizeof(vertex{})),
	)
}

// drawQuads converts quads to triangles and renders them
func (r *DirectX9Renderer) drawQuads(bufferBase unsafe.Pointer, indices []int32, color [4]float32, arrays *arrayState) {
	if len(indices) < 4 {
		return
	}

	// Convert quad indices to triangle indices
	// Each quad [0,1,2,3] becomes two triangles [0,1,2] and [0,2,3]
	numQuads := len(indices) / 4
	triIndices := make([]int32, numQuads*6)

	for q := 0; q < numQuads; q++ {
		base := q * 4
		triBase := q * 6

		triIndices[triBase+0] = indices[base+0]
		triIndices[triBase+1] = indices[base+1]
		triIndices[triBase+2] = indices[base+2]

		triIndices[triBase+3] = indices[base+0]
		triIndices[triBase+4] = indices[base+2]
		triIndices[triBase+5] = indices[base+3]
	}

	r.drawTriangles(bufferBase, triIndices, color, arrays)
}

// d3dColorFromFloat converts RGBA floats to D3D color (ARGB packed uint32)
func d3dColorFromFloat(color [4]float32) uint32 {
	r := uint32(color[0] * 255)
	g := uint32(color[1] * 255)
	b := uint32(color[2] * 255)
	a := uint32(color[3] * 255)
	return (a << 24) | (r << 16) | (g << 8) | b
}

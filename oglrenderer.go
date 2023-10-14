// oglrenderer.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"C"
	"fmt"
	"image"
	"image/draw"
	"math"
	"strings"
	"time"
	"unsafe"

	"github.com/go-gl/gl/v4.1-core/gl"
	"github.com/mmp/imgui-go/v4"
)

type OpenGL41Renderer struct {
	createdTextures map[uint32]int
	pointsTextureId uint32

	program uint32
	vao     uint32
	vbo     struct {
		indices  uint32
		position uint32
		color    uint32
		uv       uint32
	}
	params struct {
		positionArray    uint32
		colorArray       uint32
		uvArray          uint32
		projectionMatrix int32
		modelViewMatrix  int32
		sampleTexture    int32
		tex              int32
	}
	fullscreenParams struct {
		positionArray uint32
		uvArray       uint32
		time          int32
		tex           int32
	}

	fbo, fboTexture uint32
	rttRes          [2]int32
	effectsProgram  uint32
}

const vertexShader = `
#version 410 core

layout(location = 0) in vec4 inPosition;
layout(location = 1) in vec4 inColor;
layout(location = 2) in vec2 inUV;

uniform mat4 projectionMatrix;
uniform mat4 modelViewMatrix;

out vec4 v2fColor;
out vec2 v2fUV;

void main() {
    gl_Position = projectionMatrix * modelViewMatrix * vec4(inPosition.x, inPosition.y, inPosition.z, 1);
    v2fColor = inColor;
    v2fUV = inUV;
}
`

const fragmentShader = `
#version 410 core

in vec4 v2fColor;
in vec2 v2fUV;

uniform bool sampleTexture;
uniform sampler2D tex;

out vec4 outColor;

void main()
{
    if (sampleTexture) {
      outColor = v2fColor * texture(tex, v2fUV);
    } else {
      outColor = v2fColor;
    }
}
`

const vertexShaderCRT = `
#version 410 core

layout(location = 0) in vec2 inPosition;
layout(location = 1) in vec2 inUV;

out vec2 v2fUV;

void main() {
    gl_Position = vec4(inPosition.x, inPosition.y, 0.0f, 1.0f);
    v2fUV = inUV;
}
`

const fragmentShaderCRT = `
#version 410 core

in vec2 v2fUV;

uniform sampler2D framebuffer;
uniform float time;

out vec4 outColor;

// https://www.shadertoy.com/view/WdjfDy
void main() {
	vec2 uv = /*curve*/(v2fUV);
    
    vec3 col;

    // pixel shifts
    float delta = 0.0005;
    col.r = texture(framebuffer,vec2(uv.x+delta,uv.y)).x;
    col.g = texture(framebuffer,vec2(uv.x+0.000,uv.y)).y;
    col.b = texture(framebuffer,vec2(uv.x-delta,uv.y)).z;

    col *= vec3(0.95,1.05,0.95);

    // scanlines
float tt = time;
tt = 0.;
    col *= 0.95+0.05*sin(2.0*tt+uv.y*700.0);

    col *= 0.99+0.01*sin(21.0*tt);

    outColor = vec4(col,1.0);
}

/*
void main() {
  vec3 col = texture(framebuffer, v2fUV).rgb;
outColor=vec4(col*0.5, 1.0);
}
*/
`

// NewOpenGLRenderer creates an OpenGL context and compiles the
// vertex/fragment shaders.
func NewOpenGLRenderer(io imgui.IO) (Renderer, error) {
	lg.Info("Starting OpenGL41Renderer initialization")
	if err := gl.Init(); err != nil {
		return nil, fmt.Errorf("failed to initialize OpenGL: %w", err)
	}

	//gl.Enable(gl.DEBUG_OUTPUT)
	//glDebugMessageCallback(myOpenGLCallback, nullptr);

	vendor, renderer := gl.GetString(gl.VENDOR), gl.GetString(gl.RENDERER)
	v, r := (*C.char)(unsafe.Pointer(vendor)), (*C.char)(unsafe.Pointer(renderer))
	lg.Infof("OpenGL vendor %s renderer %s", C.GoString(v), C.GoString(r))

	prog, err := newProgram(vertexShader, fragmentShader)
	if err != nil {
		return nil, err
	}

	effectsProg, err := newProgram(vertexShaderCRT, fragmentShaderCRT)
	if err != nil {
		return nil, err
	}

	ogl2 := &OpenGL41Renderer{
		createdTextures: make(map[uint32]int),
		program:         prog,
		effectsProgram:  effectsProg,
	}

	ogl2.params.positionArray = uint32(gl.GetAttribLocation(ogl2.program, gl.Str("inPosition\x00")))
	ogl2.params.colorArray = uint32(gl.GetAttribLocation(ogl2.program, gl.Str("inColor\x00")))
	ogl2.params.uvArray = uint32(gl.GetAttribLocation(ogl2.program, gl.Str("inUV\x00")))
	ogl2.params.projectionMatrix = gl.GetUniformLocation(ogl2.program, gl.Str("projectionMatrix\x00"))
	ogl2.params.modelViewMatrix = gl.GetUniformLocation(ogl2.program, gl.Str("modelViewMatrix\x00"))
	ogl2.params.sampleTexture = gl.GetUniformLocation(ogl2.program, gl.Str("sampleTexture\x00"))
	ogl2.params.tex = gl.GetUniformLocation(ogl2.program, gl.Str("tex\x00"))

	ogl2.fullscreenParams.positionArray = uint32(gl.GetAttribLocation(ogl2.effectsProgram, gl.Str("inPosition\x00")))
	ogl2.fullscreenParams.uvArray = uint32(gl.GetAttribLocation(ogl2.effectsProgram, gl.Str("inUV\x00")))
	ogl2.fullscreenParams.time = gl.GetUniformLocation(ogl2.effectsProgram, gl.Str("time\x00"))
	ogl2.fullscreenParams.tex = gl.GetUniformLocation(ogl2.effectsProgram, gl.Str("framebuffer\x00"))

	oglCheck()

	gl.GenVertexArrays(1, &ogl2.vao)
	gl.BindVertexArray(ogl2.vao)
	gl.GenBuffers(1, &ogl2.vbo.indices)
	gl.GenBuffers(1, &ogl2.vbo.position)
	gl.GenBuffers(1, &ogl2.vbo.color)
	gl.GenBuffers(1, &ogl2.vbo.uv)

	gl.GenFramebuffers(1, &ogl2.fbo)
	gl.GenTextures(1, &ogl2.fboTexture)

	gl.UseProgram(prog)

	oglCheck()

	ogl2.initializePointsTexture()

	lg.Info("Finished OpenGL41Renderer initialization")
	return ogl2, nil
}

func oglCheck() {
	if err := gl.GetError(); err != gl.NO_ERROR {
		frame := Callstack()[0]
		fmt.Printf("%s:%d: GL Error %x\n", frame.File, frame.Line, err)
	}
}

func (ogl2 *OpenGL41Renderer) Dispose() {
	for texid := range ogl2.createdTextures {
		gl.DeleteTextures(1, &texid)
	}
	gl.DeleteProgram(ogl2.program)
	gl.DeleteVertexArrays(1, &ogl2.vao)
	gl.DeleteBuffers(1, &ogl2.vbo.indices)
	gl.DeleteBuffers(1, &ogl2.vbo.position)
	gl.DeleteBuffers(1, &ogl2.vbo.color)
	gl.DeleteBuffers(1, &ogl2.vbo.uv)
}

func (ogl2 *OpenGL41Renderer) createdTexture(texid uint32, bytes int) {
	_, exists := ogl2.createdTextures[texid]

	ogl2.createdTextures[texid] = bytes

	reduce := func(id uint32, bytes int, total int) int { return total + bytes }
	total := ReduceMap[uint32, int, int](ogl2.createdTextures, reduce, 0)
	mb := float32(total) / (1024 * 1024)

	if exists {
		lg.Infof("Updated tex id %d: %d bytes -> %.2f MiB of textures total", texid, bytes, mb)
	} else {
		lg.Infof("Created tex id %d: %d bytes -> %.2f MiB of textures total", texid, bytes, mb)
	}
}

func (ogl2 *OpenGL41Renderer) CreateTextureFromImage(img image.Image) uint32 {
	return ogl2.CreateTextureFromImages([]image.Image{img})
}

func (ogl2 *OpenGL41Renderer) CreateTextureFromImages(pyramid []image.Image) uint32 {
	var texid uint32
	gl.GenTextures(1, &texid)
	ogl2.UpdateTextureFromImages(texid, pyramid)
	return texid
}

func (ogl2 *OpenGL41Renderer) UpdateTextureFromImage(texid uint32, img image.Image) {
	ogl2.UpdateTextureFromImages(texid, []image.Image{img})
}

func (ogl2 *OpenGL41Renderer) UpdateTextureFromImages(texid uint32, pyramid []image.Image) {
	var lastTexture int32
	gl.GetIntegerv(gl.TEXTURE_BINDING_2D, &lastTexture)

	gl.BindTexture(gl.TEXTURE_2D, texid)
	if len(pyramid) == 1 {
		gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
	} else {
		gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR_MIPMAP_LINEAR)
	}
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
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
	oglCheck()

	ogl2.createdTexture(texid, bytes)
}

func (ogl2 *OpenGL41Renderer) DestroyTexture(texid uint32) {
	gl.DeleteTextures(1, &texid)
	delete(ogl2.createdTextures, texid)
}

func (ogl2 *OpenGL41Renderer) RenderCommandBuffer(cb *CommandBuffer) RendererStats {
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
		return math.Float32frombits(ui32())
	}

	for i < len(cb.Buf) {
		oglCheck()

		cmd := cb.Buf[i]
		i++
		switch cmd {
		case RendererLoadProjectionMatrix:
			ptr := unsafe.Pointer(&cb.Buf[i])
			gl.UniformMatrix4fv(ogl2.params.projectionMatrix, 1, false, (*float32)(ptr))
			i += 16

		case RendererLoadModelViewMatrix:
			ptr := unsafe.Pointer(&cb.Buf[i])
			gl.UniformMatrix4fv(ogl2.params.modelViewMatrix, 1, false, (*float32)(ptr))
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

			gl.DisableVertexAttribArray(ogl2.params.colorArray)
			gl.VertexAttrib4f(ogl2.params.colorArray, r, g, b, a)

		case RendererDataBuffer:
			// Skip ahead
			i += int(i32())

		case RendererEnableTexture:
			gl.ActiveTexture(gl.TEXTURE0)
			gl.BindTexture(gl.TEXTURE_2D, ui32())
			gl.Uniform1i(ogl2.params.tex, 0) // tex unit 0
			gl.Uniform1i(ogl2.params.sampleTexture, gl.TRUE)

		case RendererDisableTexture:
			gl.ActiveTexture(gl.TEXTURE0)
			gl.BindTexture(gl.TEXTURE_2D, 0)

			gl.Uniform1i(ogl2.params.sampleTexture, gl.FALSE)

		case RendererVertexArray:
			offset := ui32()
			nBytes := i32()
			nc := i32()
			stride := i32()

			ptr := unsafe.Add(unsafe.Pointer(&cb.Buf[0]), offset)

			gl.BindBuffer(gl.ARRAY_BUFFER, ogl2.vbo.position)
			gl.BufferData(gl.ARRAY_BUFFER, int(nBytes), ptr, gl.DYNAMIC_DRAW)

			gl.EnableVertexAttribArray(ogl2.params.positionArray)
			gl.VertexAttribPointer(ogl2.params.positionArray, nc, gl.FLOAT, false, stride, nil)

		case RendererDisableVertexArray:
			gl.DisableVertexAttribArray(ogl2.params.positionArray)

		case RendererRGB32Array:
			offset := ui32()
			nBytes := i32()
			nc := i32()
			stride := i32()

			ptr := unsafe.Add(unsafe.Pointer(&cb.Buf[0]), offset)

			gl.BindBuffer(gl.ARRAY_BUFFER, ogl2.vbo.color)
			gl.BufferData(gl.ARRAY_BUFFER, int(nBytes), ptr, gl.DYNAMIC_DRAW)

			gl.EnableVertexAttribArray(ogl2.params.colorArray)
			gl.VertexAttribPointer(ogl2.params.colorArray, nc, gl.FLOAT, false, stride, nil)

		case RendererRGB8Array:
			offset := ui32()
			nBytes := i32()
			nc := i32()
			stride := i32()

			ptr := unsafe.Add(unsafe.Pointer(&cb.Buf[0]), offset)

			gl.BindBuffer(gl.ARRAY_BUFFER, ogl2.vbo.color)
			gl.BufferData(gl.ARRAY_BUFFER, int(nBytes), ptr, gl.DYNAMIC_DRAW)

			gl.EnableVertexAttribArray(ogl2.params.colorArray)
			gl.VertexAttribPointer(ogl2.params.colorArray, nc, gl.UNSIGNED_BYTE, true /* normalize? */, stride, nil)

		case RendererDisableColorArray:
			gl.DisableVertexAttribArray(ogl2.params.colorArray)

		case RendererTexCoordArray:
			offset := ui32()
			nBytes := i32()
			nc := i32()
			stride := i32()

			ptr := unsafe.Add(unsafe.Pointer(&cb.Buf[0]), offset)

			gl.BindBuffer(gl.ARRAY_BUFFER, ogl2.vbo.uv)
			gl.BufferData(gl.ARRAY_BUFFER, int(nBytes), ptr, gl.DYNAMIC_DRAW)

			gl.EnableVertexAttribArray(ogl2.params.uvArray)
			gl.VertexAttribPointer(ogl2.params.uvArray, nc, gl.FLOAT, false, stride, nil)

		case RendererDisableTexCoordArray:
			gl.DisableVertexAttribArray(ogl2.params.uvArray)

		case RendererDrawPoints:
			offset := ui32()
			nBytes := i32()
			count := i32()

			ptr := unsafe.Add(unsafe.Pointer(&cb.Buf[0]), offset)

			gl.Enable(gl.BLEND)
			gl.BlendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA)

			gl.BindBuffer(gl.ELEMENT_ARRAY_BUFFER, ogl2.vbo.indices)
			gl.BufferData(gl.ELEMENT_ARRAY_BUFFER, int(nBytes), ptr, gl.DYNAMIC_DRAW)
			gl.DrawElements(gl.POINTS, count, gl.UNSIGNED_INT, nil)
			stats.nDrawCalls++
			stats.nPoints += int(count)

			gl.BlendFunc(gl.NONE, gl.NONE)
			gl.Disable(gl.BLEND)

		case RendererDrawLines:
			idxBuf := RenderBuffer{Offset: int(i32()), Size: int(i32())}
			count := i32()

			ptr := unsafe.Add(unsafe.Pointer(&cb.Buf[0]), idxBuf.Offset)
			gl.BindBuffer(gl.ELEMENT_ARRAY_BUFFER, ogl2.vbo.indices)
			gl.BufferData(gl.ELEMENT_ARRAY_BUFFER, idxBuf.Size, ptr, gl.DYNAMIC_DRAW)

			gl.DrawElements(gl.LINES, count, gl.UNSIGNED_INT, nil)

			stats.nDrawCalls++
			stats.nLines += int(count / 2)

		case RendererDrawTriangles:
			offset := ui32()
			nBytes := i32()
			count := i32()
			ptr := unsafe.Add(unsafe.Pointer(&cb.Buf[0]), offset)

			gl.BindBuffer(gl.ELEMENT_ARRAY_BUFFER, ogl2.vbo.indices)
			gl.BufferData(gl.ELEMENT_ARRAY_BUFFER, int(nBytes), ptr, gl.DYNAMIC_DRAW)
			gl.DrawElements(gl.TRIANGLES, count, gl.UNSIGNED_INT, nil)

			stats.nDrawCalls++
			stats.nTriangles += int(count / 3)

		case RendererResetState:
			gl.Disable(gl.SCISSOR_TEST)
			// viewport?
			gl.Disable(gl.BLEND)

		case RendererCallBuffer:
			idx := ui32()
			s2 := ogl2.RenderCommandBuffer(&cb.called[idx])
			stats.Merge(s2)

		case RendererStartRTT:
			res := [2]int32{i32(), i32()}

			gl.BindFramebuffer(gl.FRAMEBUFFER, ogl2.fbo)

			if res[0] > ogl2.rttRes[0] || res[1] > ogl2.rttRes[1] {
				gl.BindTexture(gl.TEXTURE_2D, ogl2.fboTexture)
				gl.TexImage2D(gl.TEXTURE_2D, 0, gl.RGBA, int32(res[0]), int32(res[1]),
					0, gl.RGBA, gl.UNSIGNED_BYTE, nil)
				gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
				gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
				gl.BindTexture(gl.TEXTURE_2D, 0)

				gl.FramebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D,
					ogl2.fboTexture, 0)

				if gl.CheckFramebufferStatus(gl.FRAMEBUFFER) != gl.FRAMEBUFFER_COMPLETE {
					lg.Errorf("Incomplete framebuffer")
				}

				ogl2.rttRes = res
			}

			drawBufs := []uint32{gl.COLOR_ATTACHMENT0}
			gl.DrawBuffers(1, &drawBufs[0])

			oglCheck()

		case RendererEndRTT:
			gl.BindFramebuffer(gl.FRAMEBUFFER, 0)
			oglCheck()

		case RendererApplyCRT:
			// FIXME: 2x
			paneExtent := Extent2D{p0: [2]float32{2 * float(), 2 * float()}, p1: [2]float32{2 * float(), 2 * float()}}

			oglCheck()

			gl.UseProgram(ogl2.effectsProgram)
			gl.BindVertexArray(ogl2.vao) // TODO: needed??

			gl.ActiveTexture(gl.TEXTURE0)
			gl.BindTexture(gl.TEXTURE_2D, ogl2.fboTexture)
			gl.Uniform1i(ogl2.fullscreenParams.tex, 0) // tex unit 0

			p := [][2]float32{[2]float32{-1, 1}, [2]float32{-1, -1}, [2]float32{1, -1},
				[2]float32{1, -1}, [2]float32{1, 1}, [2]float32{-1, 1}}
			gl.BindBuffer(gl.ARRAY_BUFFER, ogl2.vbo.position)
			gl.BufferData(gl.ARRAY_BUFFER, 2*4*len(p), unsafe.Pointer(&p[0]), gl.DYNAMIC_DRAW)
			gl.EnableVertexAttribArray(ogl2.fullscreenParams.positionArray)
			gl.VertexAttribPointer(ogl2.fullscreenParams.positionArray, 2, gl.FLOAT, false, 2*4, nil)

			tex := func(p [2]float32) [2]float32 {
				p = [2]float32{(p[0] + 1) / 2, (p[1] + 1) / 2}
				return [2]float32{
					lerp(p[0], paneExtent.p0[0], paneExtent.p1[0]) / float32(ogl2.rttRes[0]),
					lerp(p[1], paneExtent.p0[1], paneExtent.p1[1]) / float32(ogl2.rttRes[1])}
			}
			uv := make([][2]float32, len(p))
			for i, pt := range p {
				uv[i] = tex(pt)
			}
			gl.BindBuffer(gl.ARRAY_BUFFER, ogl2.vbo.uv)
			gl.BufferData(gl.ARRAY_BUFFER, 2*4*len(uv), unsafe.Pointer(&uv[0]), gl.DYNAMIC_DRAW)
			gl.EnableVertexAttribArray(ogl2.fullscreenParams.uvArray)
			gl.VertexAttribPointer(ogl2.fullscreenParams.uvArray, 2, gl.FLOAT, false, 2*4, nil)

			gl.Uniform1f(ogl2.fullscreenParams.time, float32(time.Now().Nanosecond())/1000000000)

			gl.DrawArrays(gl.TRIANGLES, 0, 6)

			gl.DisableVertexAttribArray(ogl2.fullscreenParams.positionArray)
			gl.DisableVertexAttribArray(ogl2.fullscreenParams.uvArray)
			gl.BindTexture(gl.TEXTURE_2D, 0)
			gl.UseProgram(ogl2.program)
			oglCheck()

		default:
			lg.Error("unhandled command")
		}
	}

	return stats
}

// https://github.com/go-gl/example/blob/master/gl41core-cube/cube.go
func newProgram(vertexShaderSource, fragmentShaderSource string) (uint32, error) {
	vertexShader, err := compileShader(vertexShaderSource, gl.VERTEX_SHADER)
	if err != nil {
		return 0, err
	}

	fragmentShader, err := compileShader(fragmentShaderSource, gl.FRAGMENT_SHADER)
	if err != nil {
		return 0, err
	}

	program := gl.CreateProgram()

	gl.AttachShader(program, vertexShader)
	gl.AttachShader(program, fragmentShader)
	gl.LinkProgram(program)

	var status int32
	gl.GetProgramiv(program, gl.LINK_STATUS, &status)
	if status == gl.FALSE {
		var logLength int32
		gl.GetProgramiv(program, gl.INFO_LOG_LENGTH, &logLength)

		log := strings.Repeat("\x00", int(logLength+1))
		gl.GetProgramInfoLog(program, logLength, nil, gl.Str(log))

		return 0, fmt.Errorf("failed to link program: %v", log)
	}

	gl.DeleteShader(vertexShader)
	gl.DeleteShader(fragmentShader)

	return program, nil
}

func compileShader(source string, shaderType uint32) (uint32, error) {
	shader := gl.CreateShader(shaderType)

	csources, free := gl.Strs(source)
	gl.ShaderSource(shader, 1, csources, nil)
	free()
	gl.CompileShader(shader)

	var status int32
	gl.GetShaderiv(shader, gl.COMPILE_STATUS, &status)
	if status == gl.FALSE {
		var logLength int32
		gl.GetShaderiv(shader, gl.INFO_LOG_LENGTH, &logLength)

		log := strings.Repeat("\x00", int(logLength+1))
		gl.GetShaderInfoLog(shader, logLength, nil, gl.Str(log))

		return 0, fmt.Errorf("failed to compile %v: %v", source, log)
	}

	return shader, nil
}

func (ogl *OpenGL41Renderer) initializePointsTexture() {
	// base level
	res := 128
	img := image.NewNRGBA(image.Rectangle{Max: image.Point{X: res, Y: res}})
	lookup := func(img *image.NRGBA, x, y, c int) *uint8 {
		return &img.Pix[y*img.Stride+4*x+c]
	}

	for y := 0; y < res; y++ {
		for x := 0; x < res; x++ {
			for c := 0; c < 4; c++ {
				d := sqrt(sqr(float32(x-63)) + sqr(float32(y-63)))
				*lookup(img, x, y, c) = Select(d < 62, uint8(255), uint8(0))
			}
		}
	}

	pyramid := []image.Image{img}
	for res > 1 {
		nr := res / 2
		prev := pyramid[len(pyramid)-1].(*image.NRGBA)
		img := image.NewNRGBA(image.Rectangle{Max: image.Point{X: nr, Y: nr}})
		for y := 0; y < nr; y++ {
			for x := 0; x < nr; x++ {
				for c := 0; c < 4; c++ {
					v := (int(*lookup(prev, 2*y, 2*x, c)) + int(*lookup(prev, 2*y, 2*x+1, c)) +
						int(*lookup(prev, 2*y+1, 2*x, c)) + int(*lookup(prev, 2*y+1, 2*x+1, c)) + 2) / 4
					*lookup(img, x, y, c) = uint8(v)
				}
			}
		}

		res = nr
	}

	ogl.pointsTextureId = ogl.CreateTextureFromImages(pyramid)
}

func (ogl *OpenGL41Renderer) GetPointsTextureId() uint32 {
	return ogl.pointsTextureId
}

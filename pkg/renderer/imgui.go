package renderer

import (
	"unsafe"

	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"

	"github.com/mmp/imgui-go/v4"
)

///////////////////////////////////////////////////////////////////////////
// imgui draw list conversion

// GenerateImguiCommandBuffer retrieves the imgui draw list for the current
// frame and emits corresponding commands to the provided CommandBuffer.
func GenerateImguiCommandBuffer(cb *CommandBuffer, displaySize, framebufferSize [2]float32, lg *log.Logger) {
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
	cb.LoadProjectionMatrix(math.Identity3x3().Ortho(0, float32(displayWidth), float32(displayHeight), 0))
	cb.LoadModelViewMatrix(math.Identity3x3())
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
		indexOffset := cb.IntBuffer(unsafe.Slice((*int32)(indexBufferPtr), indexBufferSizeBytes/4))

		// Copy the vertex buffer into the command buffer and specify the
		// various draw arrays.
		vertexOffset := cb.RawBuffer(unsafe.Slice((*byte)(vertexBufferPtr), vertexBufferSizeBytes))
		cb.VertexArray(vertexOffset+vertexOffsetPos, 2, vertexSize)
		cb.TexCoordArray(vertexOffset+vertexOffsetUV, 2, vertexSize)
		cb.RGB8Array(vertexOffset+vertexOffsetRGB, 4, vertexSize)

		for _, command := range commandList.Commands() {
			if command.HasUserCallback() {
				lg.Error("Unexpected user callback in imgui draw list")
			} else {
				clipRect := command.ClipRect()
				cb.Scissor(int(clipRect.X), int(fbHeight)-int(clipRect.W),
					int(clipRect.Z-clipRect.X), int(clipRect.W-clipRect.Y))
				cb.EnableTexture(uint32(command.TextureID()))
				cb.DrawTriangles(indexOffset, command.ElementCount())
			}

			indexOffset += command.ElementCount() * 4
		}
	}

	cb.DisableBlend()
	cb.ResetState()
}

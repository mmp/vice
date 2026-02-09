// pkg/renderer/imgui.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package renderer

import (
	"unsafe"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"

	"github.com/AllenDang/cimgui-go/imgui"
)

///////////////////////////////////////////////////////////////////////////
// imgui draw list conversion

// GenerateImguiCommandBuffer retrieves the imgui draw list for the current
// frame and emits corresponding commands to the provided CommandBuffer.
func GenerateImguiCommandBuffer(cb *CommandBuffer, displaySize, framebufferSize [2]float32, lg *log.Logger) {
	drawData := imgui.CurrentDrawData()

	// Avoid rendering when minimized.
	fbWidth, fbHeight := framebufferSize[0], framebufferSize[1]
	if fbWidth <= 0 || fbHeight <= 0 {
		return
	}

	// Use DisplayPos and DisplaySize from the draw data for viewport correctness.
	clipOff := drawData.DisplayPos()
	clipScale := [2]float32{
		fbWidth / displaySize[0],
		fbHeight / displaySize[1],
	}

	cb.ResetState()

	cb.LoadProjectionMatrix(math.Identity3x3().Ortho(
		clipOff.X, clipOff.X+displaySize[0],
		clipOff.Y+displaySize[1], clipOff.Y))
	cb.LoadModelViewMatrix(math.Identity3x3())
	cb.Viewport(0, 0, int(fbWidth), int(fbHeight))
	cb.Blend()

	// Get the vertex and index buffer sizes and layout information.
	vertexSize, vertexOffsetPos, vertexOffsetUV, vertexOffsetRGB := imgui.VertexBufferLayout()
	indexSize := imgui.IndexBufferLayout()

	// Handle each command list
	for _, commandList := range drawData.CommandLists() {
		vertexBufferPtr, vertexBufferSizeBytes := commandList.GetVertexBuffer()
		indexBufferPtr, indexBufferSizeBytes := commandList.GetIndexBuffer()

		// CommandBuffer only supports int32 for index buffers, so in the
		// usual case that imgui has given uint16s, create a corresponding
		// new int32 buffer.
		if indexSize != 4 {
			n := indexBufferSizeBytes / indexSize
			buf16 := unsafe.Slice((*uint16)(indexBufferPtr), n)

			buf32 := make([]int32, n)
			for i := range n {
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
				cr := command.ClipRect()
				clipX := (cr.X - clipOff.X) * clipScale[0]
				clipY := (cr.Y - clipOff.Y) * clipScale[1]
				clipZ := (cr.Z - clipOff.X) * clipScale[0]
				clipW := (cr.W - clipOff.Y) * clipScale[1]

				if clipZ <= clipX || clipW <= clipY {
					continue
				}

				cb.Scissor(int(clipX), max(int(fbHeight-clipW), 0),
					int(clipZ-clipX), int(clipW-clipY))
				cb.EnableTexture(uint32(command.TexID()))
				cb.DrawTriangles(indexOffset+int(command.IdxOffset()*4), int(command.ElemCount()))
			}
		}
	}

	cb.DisableBlend()
	cb.ResetState()
}

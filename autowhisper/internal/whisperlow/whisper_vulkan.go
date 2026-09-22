// autowhisper/internal/whisperlow/whisper_vulkan.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

//go:build vulkan

package whisperlow

// Link Vulkan GPU support for Windows builds with the "vulkan" build tag.
// This requires the Vulkan SDK to be installed and ggml-vulkan.a to be built.

/*
#cgo windows LDFLAGS: ${SRCDIR}/../../../whisper.cpp/build_go/ggml/src/ggml-vulkan/ggml-vulkan.a -lvulkan-1
#cgo linux LDFLAGS: ${SRCDIR}/../../../whisper.cpp/build_go/ggml/src/ggml-vulkan/libggml-vulkan.a -lvulkan
*/
import "C"

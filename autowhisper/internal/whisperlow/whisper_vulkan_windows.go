//go:build windows && vulkan

package whisperlow

// Link Vulkan GPU support for Windows builds with the "vulkan" build tag.
// This requires the Vulkan SDK to be installed and ggml-vulkan.a to be built.

/*
#cgo windows LDFLAGS: ${SRCDIR}/../../../whisper.cpp/build_go/ggml/src/ggml-vulkan/ggml-vulkan.a -lvulkan-1
*/
import "C"

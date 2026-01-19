//go:build !vulkan

package whisperlow

// GPU configuration defaults for non-Vulkan builds.
// When Vulkan is not available, GPU acceleration is disabled.

// GPUAvailable returns false when Vulkan support is not compiled in.
func GPUAvailable() bool {
	return false
}

// GetPreferredGPUDevice returns 0 when Vulkan is not available.
func GetPreferredGPUDevice() int {
	return 0
}

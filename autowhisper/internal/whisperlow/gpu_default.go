//go:build !vulkan

package whisperlow

import "runtime"

func init() {
	// On macOS, Metal is always used for GPU acceleration (handled by whisper.cpp internally).
	// Set gpuEnabled so the reporting code knows GPU is being used.
	if runtime.GOOS == "darwin" {
		gpuEnabled = true
	}
}

// GPU configuration defaults for non-Vulkan builds.
// When Vulkan is not available, GPU acceleration is disabled (except macOS with Metal).

// GPUAvailable returns false when Vulkan support is not compiled in.
// Note: On macOS, Metal is used but this function still returns false since
// we don't have Vulkan-style device enumeration.
func GPUAvailable() bool {
	return false
}

// GetPreferredGPUDevice returns 0 when Vulkan is not available.
func GetPreferredGPUDevice() int {
	return 0
}

// GetGPUInfo returns information about GPU acceleration status.
// On non-Vulkan builds, returns minimal info. On macOS, Metal is available
// but we don't have detailed device enumeration.
func GetGPUInfo() GPUInfo {
	info := GPUInfo{
		Enabled:       gpuEnabled,
		SelectedIndex: gpuDevice,
	}

	// On macOS, Metal is used but we don't enumerate devices the same way
	if runtime.GOOS == "darwin" {
		info.Devices = []GPUDeviceInfo{
			{
				Index:       0,
				Description: "macOS Metal GPU",
				DeviceType:  GPUDeviceTypeDiscrete, // Treat as discrete for reporting purposes
			},
		}
	}

	return info
}

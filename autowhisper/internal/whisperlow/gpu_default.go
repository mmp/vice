//go:build !vulkan

package whisperlow

import "runtime"

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

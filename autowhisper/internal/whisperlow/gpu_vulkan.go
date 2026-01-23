//go:build vulkan

package whisperlow

/*
#include <ggml-vulkan.h>
#include <stdlib.h>
#include <string.h>
*/
import "C"
import (
	"strings"
	"unsafe"
)

// VulkanDeviceInfo contains information about a Vulkan GPU device.
type VulkanDeviceInfo struct {
	Index       int
	Description string
	FreeMemory  uint64
	TotalMemory uint64
	DeviceType  GPUDeviceType
}

// IsDiscrete returns true if the device is a discrete GPU.
func (d VulkanDeviceInfo) IsDiscrete() bool {
	return d.DeviceType == GPUDeviceTypeDiscrete
}

// GetVulkanDevices returns information about all available Vulkan devices.
func GetVulkanDevices() []VulkanDeviceInfo {
	count := int(C.ggml_backend_vk_get_device_count())
	if count == 0 {
		return nil
	}

	devices := make([]VulkanDeviceInfo, count)
	for i := 0; i < count; i++ {
		desc := make([]C.char, 256)
		C.ggml_backend_vk_get_device_description(C.int(i), &desc[0], 256)

		var free, total C.size_t
		C.ggml_backend_vk_get_device_memory(C.int(i), &free, &total)

		devices[i] = VulkanDeviceInfo{
			Index:       i,
			Description: C.GoString(&desc[0]),
			FreeMemory:  uint64(free),
			TotalMemory: uint64(total),
			DeviceType:  GPUDeviceType(C.ggml_backend_vk_get_device_type(C.int(i))),
		}
	}
	return devices
}

// GetPreferredGPUDevice returns the index of the preferred GPU device.
// It prefers discrete GPUs over integrated ones, and among discrete GPUs,
// it prefers the one with the most available memory.
func GetPreferredGPUDevice() int {
	devices := GetVulkanDevices()
	if len(devices) == 0 {
		return 0
	}

	// First, try to find a discrete GPU with the most free memory
	bestDiscrete := -1
	bestDiscreteMemory := uint64(0)
	for _, dev := range devices {
		if dev.IsDiscrete() && dev.FreeMemory > bestDiscreteMemory {
			bestDiscrete = dev.Index
			bestDiscreteMemory = dev.FreeMemory
		}
	}
	if bestDiscrete >= 0 {
		return bestDiscrete
	}

	// Fall back to any GPU with most memory (integrated, virtual, etc.)
	best := 0
	bestMemory := uint64(0)
	for _, dev := range devices {
		if dev.FreeMemory > bestMemory {
			best = dev.Index
			bestMemory = dev.FreeMemory
		}
	}
	return best
}

// GPUAvailable returns true if at least one Vulkan GPU is available.
func GPUAvailable() bool {
	return int(C.ggml_backend_vk_get_device_count()) > 0
}

func init() {
	// Set the global GPU configuration when Vulkan is available
	if GPUAvailable() {
		gpuEnabled = true
		gpuDevice = GetPreferredGPUDevice()
		gpuDiscrete = GPUDeviceType(C.ggml_backend_vk_get_device_type(C.int(gpuDevice))) == GPUDeviceTypeDiscrete

		// Disable flash attention for AMD GPUs - Vulkan FA only works on NVIDIA
		// with coopmat2 extension; on AMD it falls back to CPU causing severe slowdowns
		devices := GetVulkanDevices()
		if gpuDevice < len(devices) {
			desc := devices[gpuDevice].Description
			if strings.Contains(desc, "AMD") || strings.Contains(desc, "Radeon") {
				gpuFlashAttn = false
			}
		}
	}
}

// GetGPUInfo returns detailed information about GPU acceleration status and devices.
func GetGPUInfo() GPUInfo {
	vulkanDevices := GetVulkanDevices()
	devices := make([]GPUDeviceInfo, len(vulkanDevices))
	for i, vd := range vulkanDevices {
		devices[i] = GPUDeviceInfo{
			Index:       vd.Index,
			Description: vd.Description,
			FreeMemory:  vd.FreeMemory,
			TotalMemory: vd.TotalMemory,
			DeviceType:  vd.DeviceType,
		}
	}

	return GPUInfo{
		Enabled:       gpuEnabled,
		SelectedIndex: gpuDevice,
		Devices:       devices,
	}
}

// Ensure the C import is used
var _ = unsafe.Pointer(nil)

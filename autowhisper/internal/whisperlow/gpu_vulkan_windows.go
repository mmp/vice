//go:build windows && vulkan

package whisperlow

/*
#include <ggml-vulkan.h>
#include <stdlib.h>
#include <string.h>
*/
import "C"
import "unsafe"

// VulkanDeviceInfo contains information about a Vulkan GPU device.
type VulkanDeviceInfo struct {
	Index       int
	Description string
	FreeMemory  uint64
	TotalMemory uint64
	IsDiscrete  bool
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
			IsDiscrete:  isDiscreteGPU(C.GoString(&desc[0])),
		}
	}
	return devices
}

// isDiscreteGPU heuristically determines if a GPU is discrete based on its description.
// Discrete GPUs typically have brand names like NVIDIA, AMD Radeon, etc.
// Integrated GPUs typically have "Intel" (HD/UHD/Iris), "AMD APU", or similar.
func isDiscreteGPU(description string) bool {
	// Discrete GPU indicators
	discreteIndicators := []string{
		"NVIDIA", "GeForce", "RTX", "GTX", "Quadro", "Tesla",
		"Radeon RX", "Radeon Pro",
	}
	for _, ind := range discreteIndicators {
		if containsIgnoreCase(description, ind) {
			return true
		}
	}

	// Integrated GPU indicators (if found, it's NOT discrete)
	integratedIndicators := []string{
		"Intel", "UHD", "Iris", "AMD APU", "Vega",
	}
	for _, ind := range integratedIndicators {
		if containsIgnoreCase(description, ind) {
			return false
		}
	}

	// Default to discrete if we can't determine
	return true
}

func containsIgnoreCase(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		(len(s) > 0 && len(substr) > 0 && containsLower(toLower(s), toLower(substr))))
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		} else {
			b[i] = c
		}
	}
	return string(b)
}

func containsLower(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
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
		if dev.IsDiscrete && dev.FreeMemory > bestDiscreteMemory {
			bestDiscrete = dev.Index
			bestDiscreteMemory = dev.FreeMemory
		}
	}
	if bestDiscrete >= 0 {
		return bestDiscrete
	}

	// Fall back to integrated GPU with most memory
	bestIntegrated := 0
	bestIntegratedMemory := uint64(0)
	for _, dev := range devices {
		if !dev.IsDiscrete && dev.FreeMemory > bestIntegratedMemory {
			bestIntegrated = dev.Index
			bestIntegratedMemory = dev.FreeMemory
		}
	}
	return bestIntegrated
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
	}
}

// Ensure the C import is used
var _ = unsafe.Pointer(nil)

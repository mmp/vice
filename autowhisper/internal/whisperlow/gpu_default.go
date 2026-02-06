//go:build !vulkan

package whisperlow

import (
	"bytes"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

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
				Description: getMacHardwareDescription(),
				DeviceType:  GPUDeviceTypeDiscrete, // Treat as discrete for reporting purposes
			},
		}
	}

	return info
}

// getMacHardwareDescription returns a detailed description of Mac hardware.
// Format: "Apple M4 Pro, 14 CPU / 20 GPU cores, 48GB" or similar.
func getMacHardwareDescription() string {
	chipName := getMacChipName()
	if chipName == "" {
		return "macOS Metal GPU"
	}

	cpuCores := runtime.NumCPU()
	gpuCores := getMacGPUCoreCount()
	memGB := getMacMemoryGB()

	var parts []string
	parts = append(parts, chipName)

	if gpuCores > 0 {
		parts = append(parts, strconv.Itoa(cpuCores)+" CPU / "+strconv.Itoa(gpuCores)+" GPU cores")
	} else {
		parts = append(parts, strconv.Itoa(cpuCores)+" CPU cores")
	}

	if memGB > 0 {
		parts = append(parts, strconv.Itoa(memGB)+"GB")
	}

	return strings.Join(parts, ", ")
}

// getMacChipName returns the chip name (e.g., "Apple M4 Pro") or empty string on error.
func getMacChipName() string {
	// Try sysctl first for Apple Silicon
	out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output()
	if err == nil {
		name := strings.TrimSpace(string(out))
		if name != "" {
			return name
		}
	}

	// For Apple Silicon, parse from system_profiler
	out, err = exec.Command("system_profiler", "SPHardwareDataType").Output()
	if err != nil {
		return ""
	}

	// Look for "Chip:" line
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Chip:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Chip:"))
		}
	}

	return ""
}

// getMacGPUCoreCount returns the GPU core count on Apple Silicon, or 0 if unknown.
func getMacGPUCoreCount() int {
	// Use system_profiler to get GPU core count
	out, err := exec.Command("system_profiler", "SPDisplaysDataType").Output()
	if err != nil {
		return 0
	}

	// Look for "Total Number of Cores:" line under GPU section
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Total Number of Cores:") {
			numStr := strings.TrimSpace(strings.TrimPrefix(line, "Total Number of Cores:"))
			if n, err := strconv.Atoi(numStr); err == nil {
				return n
			}
		}
	}

	// Alternative: try ioreg for Apple Silicon
	out, err = exec.Command("ioreg", "-l", "-n", "AGXAcceleratorG15X").Output()
	if err != nil {
		// Try other AGX variants
		out, _ = exec.Command("ioreg", "-l", "-n", "AGXAccelerator").Output()
	}
	if len(out) > 0 {
		// Look for gpu-core-count
		re := regexp.MustCompile(`"gpu-core-count"\s*=\s*(\d+)`)
		if matches := re.FindSubmatch(out); len(matches) > 1 {
			if n, err := strconv.Atoi(string(matches[1])); err == nil {
				return n
			}
		}
	}

	return 0
}

// getMacMemoryGB returns the system memory in GB, or 0 if unknown.
func getMacMemoryGB() int {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0
	}

	memBytes, err := strconv.ParseInt(strings.TrimSpace(string(bytes.TrimSpace(out))), 10, 64)
	if err != nil {
		return 0
	}

	return int(memBytes / (1024 * 1024 * 1024))
}

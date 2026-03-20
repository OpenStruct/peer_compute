package agent

import (
	"bufio"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// SystemResources holds auto-detected hardware capabilities.
type SystemResources struct {
	CPUCores uint32
	MemoryMB uint64
	DiskGB   uint64
	GPUCount uint32
	GPUModel string
}

// DetectResources probes the local system for CPU, memory, disk, and GPU.
// All fields have safe zero-value fallbacks; detection failures are logged as warnings.
func DetectResources() SystemResources {
	r := SystemResources{
		CPUCores: uint32(runtime.NumCPU()),
		MemoryMB: detectMemory(),
		DiskGB:   detectDisk(),
	}
	r.GPUCount, r.GPUModel = detectGPU()
	return r
}

// detectMemory returns total physical memory in MB.
func detectMemory() uint64 {
	switch runtime.GOOS {
	case "darwin":
		return detectMemoryDarwin()
	case "linux":
		return detectMemoryLinux()
	default:
		slog.Warn("memory detection not supported on this OS", "os", runtime.GOOS)
		return 0
	}
}

func detectMemoryDarwin() uint64 {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		slog.Warn("failed to detect memory on macOS", "error", err)
		return 0
	}
	bytes, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		slog.Warn("failed to parse hw.memsize", "error", err)
		return 0
	}
	return bytes / (1024 * 1024)
}

func detectMemoryLinux() uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		slog.Warn("failed to open /proc/meminfo", "error", err)
		return 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, err := strconv.ParseUint(fields[1], 10, 64)
				if err == nil {
					return kb / 1024
				}
			}
		}
	}
	slog.Warn("failed to parse MemTotal from /proc/meminfo")
	return 0
}

// detectDisk returns available disk space in GB on the root filesystem.
func detectDisk() uint64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		slog.Warn("failed to detect disk space", "error", err)
		return 0
	}
	return (stat.Bavail * uint64(stat.Bsize)) / (1024 * 1024 * 1024)
}

// detectGPU shells out to nvidia-smi to detect NVIDIA GPUs.
// Returns (0, "") if nvidia-smi is not available.
func detectGPU() (uint32, string) {
	out, err := exec.Command("nvidia-smi",
		"--query-gpu=count,name",
		"--format=csv,noheader,nounits",
	).Output()
	if err != nil {
		// nvidia-smi not found or no NVIDIA GPU — not an error
		return 0, ""
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return 0, ""
	}

	// First line: "1, NVIDIA GeForce RTX 4090"
	parts := strings.SplitN(lines[0], ", ", 2)
	count, _ := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 32)
	model := ""
	if len(parts) > 1 {
		model = strings.TrimSpace(parts[1])
	}

	return uint32(count), model
}

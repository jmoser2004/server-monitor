package sysinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// collectGPUs gathers GPU stats with a best-effort, layered strategy:
//  1. nvidia-smi for NVIDIA cards (full util/mem/temp/power).
//  2. Linux sysfs (/sys/class/drm) for integrated/AMD/Intel cards, reading
//     whatever the kernel exposes (name, clock, temp, vram). Many fields are
//     simply unavailable for integrated GPUs without privileged tools, so we
//     report -1 for anything we cannot read.
func collectGPUs() []GPUInfo {
	if gpus := nvidiaGPUs(); len(gpus) > 0 {
		return gpus
	}
	return drmGPUs()
}

func nvidiaGPUs() []GPUInfo {
	path, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil
	}
	out, err := exec.Command(path,
		"--query-gpu=name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw,clocks.sm",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil
	}
	var gpus []GPUInfo
	for i, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, ",")
		if len(f) < 7 {
			continue
		}
		gpus = append(gpus, GPUInfo{
			Index:      i,
			Name:       strings.TrimSpace(f[0]),
			Vendor:     "NVIDIA",
			Util:       parseF(f[1]),
			MemUsedMB:  parseF(f[2]),
			MemTotalMB: parseF(f[3]),
			TempC:      parseF(f[4]),
			PowerW:     parseF(f[5]),
			ClockMHz:   parseF(f[6]),
		})
	}
	return gpus
}

func drmGPUs() []GPUInfo {
	cards, _ := filepath.Glob("/sys/class/drm/card[0-9]")
	var gpus []GPUInfo
	for i, card := range cards {
		dev := filepath.Join(card, "device")
		g := GPUInfo{
			Index:      i,
			Util:       readPercent(filepath.Join(dev, "gpu_busy_percent")),
			MemUsedMB:  -1,
			MemTotalMB: -1,
			TempC:      -1,
			PowerW:     -1,
			ClockMHz:   -1,
		}
		vendor := strings.TrimSpace(readStr(filepath.Join(dev, "vendor")))
		g.Vendor, g.Name = decodePCIVendor(vendor)
		// AMD exposes VRAM usage directly.
		if v := readUint(filepath.Join(dev, "mem_info_vram_used")); v >= 0 {
			g.MemUsedMB = v / (1024 * 1024)
		}
		if v := readUint(filepath.Join(dev, "mem_info_vram_total")); v >= 0 {
			g.MemTotalMB = v / (1024 * 1024)
		}
		g.TempC = gpuTemp(dev)
		g.Name = strings.TrimSpace(g.Name)
		gpus = append(gpus, g)
	}
	return gpus
}

// gpuTemp walks the card's hwmon nodes looking for a temperature input.
func gpuTemp(dev string) float64 {
	hwmons, _ := filepath.Glob(filepath.Join(dev, "hwmon", "hwmon*", "temp1_input"))
	for _, h := range hwmons {
		if v := readUint(h); v >= 0 {
			return float64(v) / 1000.0
		}
	}
	return -1
}

// decodePCIVendor maps a PCI vendor id to a friendly name.
func decodePCIVendor(id string) (vendor, name string) {
	switch strings.ToLower(id) {
	case "0x8086":
		return "Intel", "Intel Integrated GPU"
	case "0x10de":
		return "NVIDIA", "NVIDIA GPU"
	case "0x1002":
		return "AMD", "AMD GPU"
	default:
		return "GPU", "GPU"
	}
}

func readStr(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readUint(p string) float64 {
	s := readStr(p)
	if s == "" {
		return -1
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return -1
	}
	return v
}

func readPercent(p string) float64 {
	v := readUint(p)
	return v // -1 if missing
}

func parseF(s string) float64 {
	s = strings.TrimSpace(s)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return -1
	}
	return v
}

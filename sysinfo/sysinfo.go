// Package sysinfo collects a rich snapshot of the host system's state.
//
// A Collector is stateful: it remembers the previous network and disk IO
// counters so it can report per-second rates rather than monotonic totals.
// Create one with NewCollector and call Snapshot on each tick.
package sysinfo

import (
	"sort"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	psnet "github.com/shirou/gopsutil/v3/net"
	"github.com/shirou/gopsutil/v3/process"
)

// --- Wire structs (JSON shapes sent to the dashboard) ---

type SystemInfo struct {
	Hostname     string  `json:"hostname"`
	OS           string  `json:"os"`
	Platform     string  `json:"platform"`
	Version      string  `json:"version"`
	Kernel       string  `json:"kernel"`
	Arch         string  `json:"arch"`
	UptimeSec    uint64  `json:"uptime_sec"`
	BootTime     uint64  `json:"boot_time"`
	Procs        uint64  `json:"procs"`
	Load1        float64 `json:"load1"`
	Load5        float64 `json:"load5"`
	Load15       float64 `json:"load15"`
	VirtSystem   string  `json:"virt_system"`
}

type CPUInfo struct {
	Model       string    `json:"model"`
	Physical    int       `json:"physical_cores"`
	Logical     int       `json:"logical_cores"`
	MhzBase     float64   `json:"mhz_base"`
	TotalUsage  float64   `json:"total_usage"`
	CoreUsage   []float64 `json:"core_usage"`
	TempC       float64   `json:"temp_c"`
}

type MemInfo struct {
	TotalMB     uint64  `json:"total_mb"`
	UsedMB      uint64  `json:"used_mb"`
	AvailableMB uint64  `json:"available_mb"`
	CachedMB    uint64  `json:"cached_mb"`
	BuffersMB   uint64  `json:"buffers_mb"`
	UsedPercent float64 `json:"used_percent"`
	SwapTotalMB uint64  `json:"swap_total_mb"`
	SwapUsedMB  uint64  `json:"swap_used_mb"`
	SwapPercent float64 `json:"swap_percent"`
}

type DiskInfo struct {
	Device       string  `json:"device"`
	Mountpoint   string  `json:"mountpoint"`
	Fstype       string  `json:"fstype"`
	UsedGB       float64 `json:"used_gb"`
	TotalGB      float64 `json:"total_gb"`
	FreeGB       float64 `json:"free_gb"`
	UsedPercent  float64 `json:"used_percent"`
	ReadBytesSec uint64  `json:"read_bytes_sec"`
	WriteBytesSec uint64 `json:"write_bytes_sec"`
}

type NetInfo struct {
	Interface  string `json:"interface"`
	RecvBytes  uint64 `json:"recv_bytes"`
	SentBytes  uint64 `json:"sent_bytes"`
	RecvSec    uint64 `json:"recv_sec"`
	SentSec    uint64 `json:"sent_sec"`
	Addr       string `json:"addr"`
}

type GPUInfo struct {
	Index      int     `json:"index"`
	Name       string  `json:"name"`
	Vendor     string  `json:"vendor"`
	Util       float64 `json:"util"`        // -1 if unavailable
	MemUsedMB  float64 `json:"mem_used_mb"` // -1 if unavailable
	MemTotalMB float64 `json:"mem_total_mb"`
	TempC      float64 `json:"temp_c"`      // -1 if unavailable
	PowerW     float64 `json:"power_w"`     // -1 if unavailable
	ClockMHz   float64 `json:"clock_mhz"`   // -1 if unavailable
}

type ProcessInfo struct {
	PID        int32   `json:"pid"`
	Name       string  `json:"name"`
	User       string  `json:"user"`
	Status     string  `json:"status"`
	CPUPercent float64 `json:"cpu_percent"`
	MemPercent float32 `json:"mem_percent"`
	MemMB      uint64  `json:"mem_mb"`
}

type Snapshot struct {
	Timestamp string        `json:"timestamp"`
	System    SystemInfo    `json:"system"`
	CPU       CPUInfo       `json:"cpu"`
	Memory    MemInfo       `json:"memory"`
	Disks     []DiskInfo    `json:"disks"`
	Network   []NetInfo     `json:"network"`
	GPUs      []GPUInfo     `json:"gpus"`
	Processes []ProcessInfo `json:"processes"`
}

// Collector holds the state needed to compute rates between snapshots.
type Collector struct {
	cpuModel string
	lastNet  map[string]psnet.IOCountersStat
	lastDisk map[string]disk.IOCountersStat
	lastTime time.Time
}

func NewCollector() *Collector {
	c := &Collector{
		lastNet:  map[string]psnet.IOCountersStat{},
		lastDisk: map[string]disk.IOCountersStat{},
	}
	if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
		c.cpuModel = infos[0].ModelName
	}
	return c
}

// Snapshot collects a full system snapshot. The cpuSample duration is how long
// cpu.Percent blocks while measuring per-core utilisation.
func (c *Collector) Snapshot(cpuSample time.Duration) Snapshot {
	now := time.Now()
	elapsed := now.Sub(c.lastTime).Seconds()
	if c.lastTime.IsZero() {
		elapsed = 0
	}

	snap := Snapshot{Timestamp: now.UTC().Format(time.RFC3339)}
	snap.System = c.system()
	snap.CPU = c.cpuInfo(cpuSample)
	snap.Memory = memInfo()
	snap.Disks = c.disks(elapsed)
	snap.Network = c.network(elapsed)
	snap.GPUs = collectGPUs()
	snap.Processes = topProcesses(12)

	c.lastTime = now
	return snap
}

func (c *Collector) system() SystemInfo {
	s := SystemInfo{}
	if info, err := host.Info(); err == nil {
		s.Hostname = info.Hostname
		s.OS = info.OS
		s.Platform = info.Platform + " " + info.PlatformVersion
		s.Version = info.PlatformVersion
		s.Kernel = info.KernelVersion
		s.Arch = info.KernelArch
		s.UptimeSec = info.Uptime
		s.BootTime = info.BootTime
		s.Procs = info.Procs
		s.VirtSystem = info.VirtualizationSystem
	}
	if l, err := load.Avg(); err == nil {
		s.Load1, s.Load5, s.Load15 = l.Load1, l.Load5, l.Load15
	}
	return s
}

func (c *Collector) cpuInfo(sample time.Duration) CPUInfo {
	ci := CPUInfo{Model: c.cpuModel, TempC: -1}
	if per, err := cpu.Percent(sample, true); err == nil {
		ci.CoreUsage = per
		var sum float64
		for _, v := range per {
			sum += v
		}
		if len(per) > 0 {
			ci.TotalUsage = sum / float64(len(per))
		}
	}
	if n, err := cpu.Counts(false); err == nil {
		ci.Physical = n
	}
	if n, err := cpu.Counts(true); err == nil {
		ci.Logical = n
	}
	if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
		ci.MhzBase = infos[0].Mhz
	}
	ci.TempC = cpuTemp()
	return ci
}

func cpuTemp() float64 {
	temps, err := host.SensorsTemperatures()
	if err != nil {
		return -1
	}
	// Prefer well-known CPU sensor keys, else fall back to the hottest reading.
	best := -1.0
	for _, t := range temps {
		if t.Temperature <= 0 {
			continue
		}
		k := t.SensorKey
		if k == "coretemp_package_id_0" || k == "k10temp_tctl" || k == "cpu_thermal" ||
			contains(k, "package") || contains(k, "tctl") || contains(k, "cpu") {
			return t.Temperature
		}
		if t.Temperature > best {
			best = t.Temperature
		}
	}
	return best
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func memInfo() MemInfo {
	m := MemInfo{}
	if v, err := mem.VirtualMemory(); err == nil {
		const mb = 1024 * 1024
		m.TotalMB = v.Total / mb
		m.UsedMB = v.Used / mb
		m.AvailableMB = v.Available / mb
		m.CachedMB = v.Cached / mb
		m.BuffersMB = v.Buffers / mb
		m.UsedPercent = v.UsedPercent
	}
	if s, err := mem.SwapMemory(); err == nil {
		const mb = 1024 * 1024
		m.SwapTotalMB = s.Total / mb
		m.SwapUsedMB = s.Used / mb
		m.SwapPercent = s.UsedPercent
	}
	return m
}

func (c *Collector) disks(elapsed float64) []DiskInfo {
	parts, err := disk.Partitions(false)
	if err != nil {
		return nil
	}
	// Map mountpoint usage.
	var out []DiskInfo
	seen := map[string]bool{}
	const gb = 1024 * 1024 * 1024
	for _, p := range parts {
		if seen[p.Device] {
			continue // skip bind mounts of the same device
		}
		u, err := disk.Usage(p.Mountpoint)
		if err != nil || u.Total == 0 {
			continue
		}
		seen[p.Device] = true
		out = append(out, DiskInfo{
			Device:      p.Device,
			Mountpoint:  p.Mountpoint,
			Fstype:      p.Fstype,
			UsedGB:      float64(u.Used) / gb,
			TotalGB:     float64(u.Total) / gb,
			FreeGB:      float64(u.Free) / gb,
			UsedPercent: u.UsedPercent,
		})
	}

	// IO rates keyed by device basename.
	if io, err := disk.IOCounters(); err == nil {
		for i := range out {
			name := basename(out[i].Device)
			cur, ok := io[name]
			if !ok {
				continue
			}
			if prev, ok := c.lastDisk[name]; ok && elapsed > 0 {
				out[i].ReadBytesSec = perSec(cur.ReadBytes, prev.ReadBytes, elapsed)
				out[i].WriteBytesSec = perSec(cur.WriteBytes, prev.WriteBytes, elapsed)
			}
		}
		c.lastDisk = io
	}
	return out
}

func (c *Collector) network(elapsed float64) []NetInfo {
	counters, err := psnet.IOCounters(true)
	if err != nil {
		return nil
	}
	addrs := map[string]string{}
	if ifaces, err := psnet.Interfaces(); err == nil {
		for _, iface := range ifaces {
			for _, a := range iface.Addrs {
				ip := a.Addr
				if isIPv4(ip) {
					addrs[iface.Name] = stripCIDR(ip)
					break
				}
			}
		}
	}

	var out []NetInfo
	for _, cur := range counters {
		if cur.Name == "lo" || (cur.BytesRecv == 0 && cur.BytesSent == 0) {
			continue
		}
		ni := NetInfo{
			Interface: cur.Name,
			RecvBytes: cur.BytesRecv,
			SentBytes: cur.BytesSent,
			Addr:      addrs[cur.Name],
		}
		if prev, ok := c.lastNet[cur.Name]; ok && elapsed > 0 {
			ni.RecvSec = perSec(cur.BytesRecv, prev.BytesRecv, elapsed)
			ni.SentSec = perSec(cur.BytesSent, prev.BytesSent, elapsed)
		}
		c.lastNet[cur.Name] = cur
		out = append(out, ni)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Interface < out[j].Interface })
	return out
}

func topProcesses(n int) []ProcessInfo {
	procs, err := process.Processes()
	if err != nil {
		return nil
	}
	list := make([]ProcessInfo, 0, len(procs))
	for _, p := range procs {
		cpuPct, _ := p.CPUPercent()
		memPct, _ := p.MemoryPercent()
		name, _ := p.Name()
		if name == "" {
			continue
		}
		user, _ := p.Username()
		var memMB uint64
		if mi, err := p.MemoryInfo(); err == nil && mi != nil {
			memMB = mi.RSS / 1024 / 1024
		}
		status := ""
		if st, err := p.Status(); err == nil && len(st) > 0 {
			status = st[0]
		}
		list = append(list, ProcessInfo{
			PID:        p.Pid,
			Name:       name,
			User:       user,
			Status:     status,
			CPUPercent: cpuPct,
			MemPercent: memPct,
			MemMB:      memMB,
		})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].CPUPercent != list[j].CPUPercent {
			return list[i].CPUPercent > list[j].CPUPercent
		}
		return list[i].MemPercent > list[j].MemPercent
	})
	if n > len(list) {
		n = len(list)
	}
	return list[:n]
}

// --- helpers ---

func perSec(cur, prev uint64, elapsed float64) uint64 {
	if cur < prev || elapsed <= 0 {
		return 0
	}
	return uint64(float64(cur-prev) / elapsed)
}

func basename(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

func isIPv4(addr string) bool {
	dots := 0
	for _, r := range addr {
		if r == ':' {
			return false
		}
		if r == '.' {
			dots++
		}
	}
	return dots == 3
}

func stripCIDR(addr string) string {
	for i, r := range addr {
		if r == '/' {
			return addr[:i]
		}
	}
	return addr
}

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	psnet "github.com/shirou/gopsutil/v3/net"
	"github.com/shirou/gopsutil/v3/process"
)

// --- Structs ---

type SystemInfo struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Version  string `json:"version"`
	Uptime   string `json:"uptime"`
	Kernel   string `json:"kernel"`
}

type CPUInfo struct {
	CoreUsage []float64 `json:"core_usage"`
}

type MemInfo struct {
	TotalMB     uint64  `json:"total_mb"`
	UsedMB      uint64  `json:"used_mb"`
	AvailableMB uint64  `json:"available_mb"`
	UsedPercent float64 `json:"used_percent"`
}

type DiskInfo struct {
	Mountpoint  string  `json:"mountpoint"`
	Fstype      string  `json:"fstype"`
	UsedGB      uint64  `json:"used_gb"`
	TotalGB     uint64  `json:"total_gb"`
	UsedPercent float64 `json:"used_percent"`
}

type NetInfo struct {
	Interface string `json:"interface"`
	RecvMB    uint64 `json:"recv_mb"`
	SentMB    uint64 `json:"sent_mb"`
}

type ProcessInfo struct {
	PID        int32   `json:"pid"`
	Name       string  `json:"name"`
	CPUPercent float64 `json:"cpu_percent"`
	MemPercent float32 `json:"mem_percent"`
}

// Envelope sent to the client each tick
type Snapshot struct {
	Timestamp string        `json:"timestamp"`
	System    SystemInfo    `json:"system"`
	CPU       CPUInfo       `json:"cpu"`
	Memory    MemInfo       `json:"memory"`
	Disks     []DiskInfo    `json:"disks"`
	Network   []NetInfo     `json:"network"`
	Processes []ProcessInfo `json:"processes"`
}

// --- Collectors ---

func getSysInfo() (SystemInfo, error) {
	info, err := host.Info()
	if err != nil {
		return SystemInfo{}, err
	}
	return SystemInfo{
		Hostname: info.Hostname,
		OS:       info.Platform,
		Version:  info.PlatformVersion,
		Uptime:   (time.Duration(info.Uptime) * time.Second).String(),
		Kernel:   info.KernelVersion,
	}, nil
}

func getCPUInfo(sampleDuration time.Duration) (CPUInfo, error) {
	percentages, err := cpu.Percent(sampleDuration, true)
	if err != nil {
		return CPUInfo{}, err
	}
	return CPUInfo{CoreUsage: percentages}, nil
}

func getMemInfo() (MemInfo, error) {
	v, err := mem.VirtualMemory()
	if err != nil {
		return MemInfo{}, err
	}
	return MemInfo{
		TotalMB:     v.Total / 1024 / 1024,
		UsedMB:      v.Used / 1024 / 1024,
		AvailableMB: v.Available / 1024 / 1024,
		UsedPercent: v.UsedPercent,
	}, nil
}

func getDiskInfo() ([]DiskInfo, error) {
	partitions, err := disk.Partitions(false)
	if err != nil {
		return nil, err
	}
	var disks []DiskInfo
	for _, p := range partitions {
		usage, err := disk.Usage(p.Mountpoint)
		if err != nil {
			continue
		}
		disks = append(disks, DiskInfo{
			Mountpoint:  p.Mountpoint,
			Fstype:      p.Fstype,
			UsedGB:      usage.Used / 1024 / 1024 / 1024,
			TotalGB:     usage.Total / 1024 / 1024 / 1024,
			UsedPercent: usage.UsedPercent,
		})
	}
	return disks, nil
}

func getNetInfo() ([]NetInfo, error) {
	counters, err := psnet.IOCounters(true)
	if err != nil {
		return nil, err
	}
	var ifaces []NetInfo
	for _, c := range counters {
		ifaces = append(ifaces, NetInfo{
			Interface: c.Name,
			RecvMB:    c.BytesRecv / 1024 / 1024,
			SentMB:    c.BytesSent / 1024 / 1024,
		})
	}
	return ifaces, nil
}

func getTopProcesses(n int) ([]ProcessInfo, error) {
	procs, err := process.Processes()
	if err != nil {
		return nil, err
	}
	var list []ProcessInfo
	for _, p := range procs {
		c, _ := p.CPUPercent()
		m, _ := p.MemoryPercent()
		name, _ := p.Name()
		list = append(list, ProcessInfo{PID: p.Pid, Name: name, CPUPercent: c, MemPercent: m})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].CPUPercent > list[j].CPUPercent
	})
	if n > len(list) {
		n = len(list)
	}
	return list[:n], nil
}

func collectSnapshot() (Snapshot, error) {
	snap := Snapshot{Timestamp: time.Now().UTC().Format(time.RFC3339)}

	sys, err := getSysInfo()
	if err != nil {
		return snap, fmt.Errorf("sysinfo: %w", err)
	}
	snap.System = sys

	// CPU sample blocks for 500ms — accounts for most of the tick time
	cpuInfo, err := getCPUInfo(500 * time.Millisecond)
	if err != nil {
		return snap, fmt.Errorf("cpu: %w", err)
	}
	snap.CPU = cpuInfo

	memInfo, err := getMemInfo()
	if err != nil {
		return snap, fmt.Errorf("mem: %w", err)
	}
	snap.Memory = memInfo

	disks, err := getDiskInfo()
	if err != nil {
		return snap, fmt.Errorf("disk: %w", err)
	}
	snap.Disks = disks

	ifaces, err := getNetInfo()
	if err != nil {
		return snap, fmt.Errorf("net: %w", err)
	}
	snap.Network = ifaces

	procs, err := getTopProcesses(10)
	if err != nil {
		return snap, fmt.Errorf("procs: %w", err)
	}
	snap.Processes = procs

	return snap, nil
}

// --- WebSocket ---

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Println("Upgrade error:", err)
		return
	}
	defer conn.Close()
	fmt.Printf("Client connected: %s\n", r.RemoteAddr)

	// Read incoming messages in a goroutine so we detect disconnects
	// while the main loop is blocked on collectSnapshot
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			fmt.Printf("Client disconnected: %s\n", r.RemoteAddr)
			return
		case <-ticker.C:
			snap, err := collectSnapshot()
			if err != nil {
				fmt.Println("Snapshot error:", err)
				continue
			}
			if err := conn.WriteJSON(snap); err != nil {
				fmt.Println("Write error:", err)
				return
			}
		}
	}
}

func main() {
	// Sanity-check: print one snapshot to stdout on startup
	snap, err := collectSnapshot()
	if err != nil {
		fmt.Println("Initial snapshot failed:", err)
	} else {
		b, _ := json.MarshalIndent(snap, "", "  ")
		fmt.Println(string(b))
	}

	http.Handle("/", http.FileServer(http.Dir("./static")))
	http.HandleFunc("/ws", wsHandler)
	fmt.Println("\nWebSocket server listening on :8080/ws")
	http.ListenAndServe(":8080", nil)
}

package output

import (
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

// SystemInfo 用于承载系统和磁盘相关信息的数据结构
type SystemInfo struct {
	HostInfo      *host.InfoStat         `json:"host_info"`
	CPU           []float64              `json:"cpu_usage"`
	CPUInfo       []cpu.InfoStat         `json:"cpu_info"`
	Memory        *mem.VirtualMemoryStat `json:"memory_usage"`
	DiskUsage     []disk.UsageStat       `json:"disk_usage"`
	NetIOCounters []net.IOCountersStat   `json:"network_io"`
}

type NetworkStats struct {
	Name        string
	BytesSent   uint64
	BytesRecv   uint64
	PacketsSent uint64
	PacketsRecv uint64
	SendRate    float64 // Bytes per second
	RecvRate    float64 // Bytes per second
}

type DiskIOStats struct {
	Name       string
	ReadBytes  uint64
	WriteBytes uint64
	ReadCount  uint64
	WriteCount uint64
	IoTime     uint64 // time spent doing I/Os (ms)
}

type DiskIOSpeed struct {
	Name           string
	ReadSpeed      float64 // bytes per second
	WriteSpeed     float64 // bytes per second
	ReadOpsPerSec  float64
	WriteOpsPerSec float64
	AvgIoLatency   float64 // milliseconds
}
type NetStats struct {
	Name        string
	BytesSent   uint64
	BytesRecv   uint64
	PacketsSent uint64
	PacketsRecv uint64
}

type NetSpeed struct {
	Name     string
	SentRate float64 // bytes per second
	RecvRate float64 // bytes per second
}

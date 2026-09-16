package cluster

import (
	"bufio"
	"context"
	"encoding/hex"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	"oneinstack/internal/buildinfo"
	"oneinstack/internal/services/monitoring"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
)

type ControllerNode struct {
	ID            string    `json:"id"`
	Local         bool      `json:"local"`
	Role          string    `json:"role"`
	Name          string    `json:"name"`
	Hostname      string    `json:"hostname"`
	Status        string    `json:"status"`
	Enabled       bool      `json:"enabled"`
	SystemID      string    `json:"systemId,omitempty"`
	SystemVersion string    `json:"systemVersion,omitempty"`
	Architecture  string    `json:"architecture,omitempty"`
	PanelVersion  string    `json:"panelVersion,omitempty"`
	AgentVersion  string    `json:"agentVersion,omitempty"`
	LastSeenAt    time.Time `json:"lastSeenAt"`
	HostSnapshot
}

var localControllerCollector = monitoring.NewSystemCollector()

func CollectLocalController(ctx context.Context) (ControllerNode, error) {
	hostname, _ := os.Hostname()
	now := time.Now()
	snapshot, systemID, systemVersion, err := collectHostSnapshot(ctx, localControllerCollector)
	return ControllerNode{
		ID: "local", Local: true, Role: ClusterRoleController, Name: hostname,
		Hostname: hostname, Status: "online", Enabled: true, SystemID: systemID,
		SystemVersion: systemVersion, Architecture: runtime.GOARCH,
		PanelVersion: buildinfo.Version, AgentVersion: buildinfo.Version,
		LastSeenAt: now, HostSnapshot: snapshot,
	}, err
}

func collectHostSnapshot(ctx context.Context, collector *monitoring.SystemCollector) (HostSnapshot, string, string, error) {
	sample, err := collector.Collect(ctx)
	if err != nil {
		return HostSnapshot{}, "", "", err
	}
	virtualMemory, _ := mem.VirtualMemoryWithContext(ctx)
	rootDisk, _ := disk.UsageWithContext(ctx, "/")
	cores, _ := cpu.CountsWithContext(ctx, true)
	hostInfo, _ := host.InfoWithContext(ctx)
	uptime, _ := host.UptimeWithContext(ctx)
	interfaceName, ipAddress, subnetMask, macAddress := primaryNetworkAddress()
	snapshot := HostSnapshot{
		CPUPercent: sample.CPUPercent, CPUTotalCores: cores,
		CPUUsedCores:  float64(cores) * sample.CPUPercent / 100,
		MemoryPercent: sample.MemoryPercent, DiskPercent: sample.DiskPercent,
		NetworkRecvBPS: sample.NetworkReceiveBPS, NetworkSendBPS: sample.NetworkSendBPS,
		UptimeSeconds: uptime, IPAddress: ipAddress, SubnetMask: subnetMask,
		Gateway: defaultGateway(), MACAddress: macAddress, InterfaceName: interfaceName,
	}
	if virtualMemory != nil {
		snapshot.MemoryUsedBytes, snapshot.MemoryTotalBytes = virtualMemory.Used, virtualMemory.Total
	}
	if rootDisk != nil {
		snapshot.DiskUsedBytes, snapshot.DiskTotalBytes = rootDisk.Used, rootDisk.Total
	}
	if hostInfo == nil {
		return snapshot, "", "", nil
	}
	return snapshot, hostInfo.Platform, hostInfo.PlatformVersion, nil
}

func primaryNetworkAddress() (string, string, string, string) {
	interfaces, _ := net.Interfaces()
	for _, item := range interfaces {
		if item.Flags&net.FlagUp == 0 || item.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, _ := item.Addrs()
		for _, address := range addresses {
			ipNet, ok := address.(*net.IPNet)
			if !ok || ipNet.IP.To4() == nil {
				continue
			}
			return item.Name, ipNet.IP.String(), net.IP(ipNet.Mask).String(), item.HardwareAddr.String()
		}
	}
	return "", "", "", ""
}

func defaultGateway() string {
	file, err := os.Open("/proc/net/route")
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || fields[1] != "00000000" || len(fields[2]) != 8 {
			continue
		}
		bytes, err := hex.DecodeString(fields[2])
		if err == nil && len(bytes) == 4 {
			return net.IPv4(bytes[3], bytes[2], bytes[1], bytes[0]).String()
		}
	}
	return ""
}

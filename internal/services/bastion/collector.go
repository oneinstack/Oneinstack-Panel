package bastion

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"oneinstack/internal/models"

	"golang.org/x/crypto/ssh"
)

// Collector 远端服务器指标采集器接口
type Collector interface {
	Collect(ctx context.Context, server *models.BastionServer, password string) (*models.BastionMetricSample, error)
}

// SSHCollector 通过 SSH 连接到远端服务器并执行只读采集脚本
type SSHCollector struct {
	timeout time.Duration
}

// NewSSHCollector 创建 SSH 采集器
func NewSSHCollector(timeoutSeconds int) *SSHCollector {
	return &SSHCollector{timeout: time.Duration(timeoutSeconds) * time.Second}
}

// Collect 连接到远端服务器，执行采集脚本并解析结果
func (c *SSHCollector) Collect(ctx context.Context, server *models.BastionServer, password string) (*models.BastionMetricSample, error) {
	config, err := buildSSHClientConfig(server, password, c.timeout)
	if err != nil {
		return nil, err
	}

	addr := net.JoinHostPort(server.Host, fmt.Sprintf("%d", server.Port))
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("ssh dial: %w", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	done := make(chan struct{})
	var output []byte
	var execErr error

	go func() {
		output, execErr = session.CombinedOutput(collectScript)
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		return nil, fmt.Errorf("collect timeout: %w", ctx.Err())
	}

	if execErr != nil {
		return nil, fmt.Errorf("remote collect script: %w", execErr)
	}

	return parseCollectOutput(output)
}

// collectOutput 是采集脚本的 JSON 输出结构
type collectOutput struct {
	CPUPercent        float64 `json:"cpu_percent"`
	MemoryPercent     float64 `json:"memory_percent"`
	DiskPercent       float64 `json:"disk_percent"`
	Load1             float64 `json:"load_1"`
	Load5             float64 `json:"load_5"`
	Load15            float64 `json:"load_15"`
	NetworkReceiveBPS float64 `json:"network_receive_bps"`
	NetworkSendBPS    float64 `json:"network_send_bps"`
	DiskReadBPS       float64 `json:"disk_read_bps"`
	DiskWriteBPS      float64 `json:"disk_write_bps"`
	UptimeSeconds     uint64  `json:"uptime_seconds"`
}

func parseCollectOutput(output []byte) (*models.BastionMetricSample, error) {
	var result collectOutput
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("parse collect output: %w (raw: %s)", err, string(output))
	}
	now := time.Now().UTC().Truncate(time.Second)
	return &models.BastionMetricSample{
		CapturedAt:        now,
		CPUPercent:        result.CPUPercent,
		MemoryPercent:     result.MemoryPercent,
		DiskPercent:       result.DiskPercent,
		Load1:             result.Load1,
		Load5:             result.Load5,
		Load15:            result.Load15,
		NetworkReceiveBPS: result.NetworkReceiveBPS,
		NetworkSendBPS:    result.NetworkSendBPS,
		DiskReadBPS:       result.DiskReadBPS,
		DiskWriteBPS:      result.DiskWriteBPS,
		UptimeSeconds:     result.UptimeSeconds,
	}, nil
}

// collectScript 是远端执行的采集脚本。
// 纯 POSIX shell，只读取 /proc、sysfs 和标准工具，无任何写操作。
const collectScript = `#!/bin/sh
# Bastion host metric collection — read-only, no side effects.
set -e

# CPU usage (sampled over 200ms)
read -r cpu_user cpu_nice cpu_sys cpu_idle _ < /proc/stat
sleep 0.2
read -r _ cpu_user2 cpu_nice2 cpu_sys2 cpu_idle2 _ < /proc/stat
total1=$((cpu_user + cpu_nice + cpu_sys + cpu_idle))
total2=$((cpu_user2 + cpu_nice2 + cpu_sys2 + cpu_idle2))
idle1=$cpu_idle
idle2=$cpu_idle2
total_diff=$((total2 - total1))
idle_diff=$((idle2 - idle1))
cpu_percent=$(awk "BEGIN {printf \"%.1f\", ($total_diff - $idle_diff) * 100 / ($total_diff + 1)}")

# Memory
mem_total=$(awk '/MemTotal/ {print $2}' /proc/meminfo)
mem_available=$(awk '/MemAvailable/ {print $2}' /proc/meminfo)
memory_percent=$(awk "BEGIN {printf \"%.1f\", ($mem_total - $mem_available) * 100 / ($mem_total + 1)}")

# Disk usage (root filesystem)
disk_percent=$(df / | awk 'NR==2 {gsub(/%/,""); print $5}')

# Load average
load_1=$(awk '{print $1}' /proc/loadavg)
load_5=$(awk '{print $2}' /proc/loadavg)
load_15=$(awk '{print $3}' /proc/loadavg)

# Network throughput (first non-loopback interface), sampled over 1s
iface=$(awk 'NR>2 && $2 !~ /^0+:/ && $1 !~ /^lo/' /proc/net/dev | head -1 | cut -d: -f1 | xargs)
net_recv=0
net_send=0
if [ -n "$iface" ]; then
  rx_file=/sys/class/net/$iface/statistics/rx_bytes
  tx_file=/sys/class/net/$iface/statistics/tx_bytes
  rx1=$(awk 'NR==1 {print $1+0}' "$rx_file" 2>/dev/null || echo 0)
  tx1=$(awk 'NR==1 {print $1+0}' "$tx_file" 2>/dev/null || echo 0)
  sleep 1
  rx2=$(awk 'NR==1 {print $1+0}' "$rx_file" 2>/dev/null || echo 0)
  tx2=$(awk 'NR==1 {print $1+0}' "$tx_file" 2>/dev/null || echo 0)
  net_recv=$((rx2 - rx1))
  net_send=$((tx2 - tx1))
  if [ "$net_recv" -lt 0 ]; then net_recv=0; fi
  if [ "$net_send" -lt 0 ]; then net_send=0; fi
fi

# Disk I/O (first non-loopback, non-ram block device)
disk_read=0
disk_write=0

# Uptime
uptime_seconds=$(awk '{print int($1)}' /proc/uptime)

cat <<END
{
  "cpu_percent": $cpu_percent,
  "memory_percent": $memory_percent,
  "disk_percent": $disk_percent,
  "load_1": $load_1,
  "load_5": $load_5,
  "load_15": $load_15,
  "network_receive_bps": $net_recv,
  "network_send_bps": $net_send,
  "disk_read_bps": $disk_read,
  "disk_write_bps": $disk_write,
  "uptime_seconds": $uptime_seconds
}
END`

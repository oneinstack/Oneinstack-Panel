package monitoring

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"oneinstack/internal/models"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
)

type Collector interface {
	Collect(context.Context) (*models.MetricSample, error)
}

type counterSnapshot struct {
	at                  time.Time
	netReceive, netSend uint64
	diskRead, diskWrite uint64
}

type SystemCollector struct {
	mu       sync.Mutex
	previous *counterSnapshot
	now      func() time.Time
}

func NewSystemCollector() *SystemCollector {
	return &SystemCollector{now: time.Now}
}

func (collector *SystemCollector) Collect(ctx context.Context) (*models.MetricSample, error) {
	cpuValues, err := cpu.PercentWithContext(ctx, 200*time.Millisecond, false)
	if err != nil || len(cpuValues) == 0 {
		if err == nil {
			err = errors.New("CPU usage is unavailable")
		}
		return nil, err
	}
	memory, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return nil, err
	}
	rootDisk, err := disk.UsageWithContext(ctx, "/")
	if err != nil {
		return nil, err
	}
	loadAverage, err := load.AvgWithContext(ctx)
	if err != nil {
		return nil, err
	}
	network, err := gnet.IOCountersWithContext(ctx, false)
	if err != nil {
		return nil, err
	}
	diskCounters, err := disk.IOCountersWithContext(ctx)
	if err != nil {
		return nil, err
	}
	current := counterSnapshot{at: collector.now().UTC()}
	if len(network) > 0 {
		current.netReceive = network[0].BytesRecv
		current.netSend = network[0].BytesSent
	}
	for _, item := range diskCounters {
		current.diskRead += item.ReadBytes
		current.diskWrite += item.WriteBytes
	}

	sample := &models.MetricSample{
		CapturedAt: current.at, CPUPercent: finite(cpuValues[0]),
		MemoryPercent: finite(memory.UsedPercent), DiskPercent: finite(rootDisk.UsedPercent),
		Load1: finite(loadAverage.Load1), Load5: finite(loadAverage.Load5), Load15: finite(loadAverage.Load15),
	}
	collector.mu.Lock()
	if previous := collector.previous; previous != nil {
		elapsed := current.at.Sub(previous.at).Seconds()
		if elapsed > 0 {
			sample.NetworkReceiveBPS = rate(current.netReceive, previous.netReceive, elapsed)
			sample.NetworkSendBPS = rate(current.netSend, previous.netSend, elapsed)
			sample.DiskReadBPS = rate(current.diskRead, previous.diskRead, elapsed)
			sample.DiskWriteBPS = rate(current.diskWrite, previous.diskWrite, elapsed)
		}
	}
	collector.previous = &current
	collector.mu.Unlock()
	return sample, nil
}

func rate(current, previous uint64, seconds float64) float64 {
	if current < previous || seconds <= 0 {
		return 0
	}
	return finite(float64(current-previous) / seconds)
}

func finite(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	return value
}

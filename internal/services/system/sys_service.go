package system

import (
	"fmt"
	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/router/input"
	"oneinstack/router/output"
	"os"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/spf13/viper"
)

func GetSystemMonitor() (map[string]interface{}, error) {
	ls, err := GetNetIOCounters()
	if err != nil {
		return nil, err
	}
	ds, err := GetDiskIOCounters()
	if err != nil {
		return nil, err
	}
	res := map[string]interface{}{
		"network": ls,
		"disk":    ds,
	}
	return res, nil
}

func GetNetIOCounters() ([]output.NetworkStats, error) {
	// Get initial network IO counters
	initialStats, err := getNetIOCounters()
	if err != nil {
		return nil, err
	}

	time.Sleep(100 * time.Millisecond) // Wait for 2 seconds to measure speed
	totalBytesSent := uint64(0)
	totalBytesRecv := uint64(0)
	totalPacketsSent := uint64(0)
	totalPacketsRecv := uint64(0)
	m := map[string]*output.NetworkStats{}
	for _, v := range initialStats {
		m[v.Name] = &output.NetworkStats{
			Name:        v.Name,
			BytesSent:   v.BytesSent,
			BytesRecv:   v.BytesRecv,
			PacketsSent: v.PacketsSent,
			PacketsRecv: v.PacketsRecv,
		}
		totalBytesSent += v.BytesSent
		totalBytesRecv += v.BytesRecv
		totalPacketsSent += v.PacketsSent
		totalPacketsRecv += v.PacketsRecv
	}
	// Get updated network IO counters
	updatedStats, err := getNetIOCounters()
	if err != nil {
		return nil, err
	}

	// Calculate the speed
	speeds, allSpeed, err := calculateSpeed(initialStats, updatedStats, 100*time.Millisecond)
	if err != nil {
		return nil, err
	}
	for _, speed := range speeds {
		ns := m[speed.Name]
		ns.SendRate = speed.SentRate
		ns.RecvRate = speed.RecvRate
		m[speed.Name] = ns
	}
	all := &output.NetworkStats{
		Name:        "all",
		BytesSent:   totalBytesSent,
		BytesRecv:   totalBytesRecv,
		PacketsSent: totalPacketsSent,
		PacketsRecv: totalPacketsRecv,
		SendRate:    allSpeed.SentRate,
		RecvRate:    allSpeed.RecvRate,
	}
	m["all"] = all
	ls := []output.NetworkStats{}
	for _, v := range m {
		ls = append(ls, *v)
	}
	return ls, nil
}

func GetDiskIOCounters() ([]*output.DiskIOSpeed, error) {
	// Get initial disk IO counters
	initialStats, err := getDiskIOCounters()
	if err != nil {
		return nil, err
	}

	time.Sleep(500 * time.Millisecond) // Wait for 2 seconds to measure speed

	// Get updated disk IO counters
	updatedStats, err := getDiskIOCounters()
	if err != nil {
		return nil, err
	}

	// Calculate the speed and latency
	speeds, allSpeed, err := calculateDiskIOSpeed(initialStats, updatedStats, 500*time.Millisecond)
	if err != nil {
		return nil, err
	}
	allSpeed.Name = "all"
	speeds = append(speeds, allSpeed)
	return speeds, nil
}

// GetSystemInfo 获取系统信息和磁盘使用情况
func GetSystemInfo() (*output.SystemInfo, error) {
	info, err := host.Info()
	if err != nil {
		return nil, err
	}
	// CPU Info
	cpuInfo, err := cpu.Info()
	if err != nil {
		return nil, err
	}
	// CPU Usage
	cpuPercent, _ := cpu.Percent(time.Second, false)

	// Memory Info
	vmem, err := mem.VirtualMemory()
	if err != nil {
		return nil, err
	}
	// Disk Info
	partitions, err := disk.Partitions(true)
	if err != nil {
		return nil, err
	}
	var diskUsages []disk.UsageStat
	for _, partition := range partitions {
		usage, err := disk.Usage(partition.Mountpoint)
		if err != nil {
			return nil, err
		}
		diskUsages = append(diskUsages, *usage)
	}

	// Network Info
	interfaces, err := net.IOCounters(true)
	if err != nil {
		return nil, err
	}
	sysinfo := &output.SystemInfo{
		HostInfo:      info,
		CPUInfo:       cpuInfo,
		CPU:           cpuPercent,
		Memory:        vmem,
		DiskUsage:     diskUsages,
		NetIOCounters: interfaces,
	}
	return sysinfo, nil
}

func getNetIOCounters() ([]*output.NetStats, error) {
	ioCounters, err := net.IOCounters(true)
	if err != nil {
		return nil, err
	}

	var stats []*output.NetStats
	for _, v := range ioCounters {
		stats = append(stats, &output.NetStats{
			Name:        v.Name,
			BytesSent:   v.BytesSent,
			BytesRecv:   v.BytesRecv,
			PacketsSent: v.PacketsSent,
			PacketsRecv: v.PacketsRecv,
		})
	}
	return stats, nil
}

func calculateSpeed(oldStats, newStats []*output.NetStats, duration time.Duration) ([]*output.NetSpeed, *output.NetSpeed, error) {
	var allSpeed output.NetSpeed
	allSpeed.SentRate = 0.0
	allSpeed.RecvRate = 0.0

	speeds := make([]*output.NetSpeed, len(oldStats))
	for i, oldStat := range oldStats {
		newStat := newStats[i]
		if oldStat.Name != newStat.Name {
			return nil, nil, fmt.Errorf("network interface order changed")
		}

		sentRate := float64(newStat.BytesSent-oldStat.BytesSent) / duration.Seconds()
		recvRate := float64(newStat.BytesRecv-oldStat.BytesRecv) / duration.Seconds()

		allSpeed.SentRate += sentRate
		allSpeed.RecvRate += recvRate

		speeds[i] = &output.NetSpeed{
			Name:     oldStat.Name,
			SentRate: sentRate,
			RecvRate: recvRate,
		}
	}
	return speeds, &allSpeed, nil
}

func getDiskIOCounters() ([]*output.DiskIOStats, error) {
	ioCounters, err := disk.IOCounters()
	if err != nil {
		return nil, err
	}

	var stats []*output.DiskIOStats
	for name, v := range ioCounters {
		stats = append(stats, &output.DiskIOStats{
			Name:       name,
			ReadBytes:  v.ReadBytes,
			WriteBytes: v.WriteBytes,
			ReadCount:  v.ReadCount,
			WriteCount: v.WriteCount,
			IoTime:     v.IoTime,
		})
	}
	return stats, nil
}

func calculateDiskIOSpeed(oldStats, newStats []*output.DiskIOStats, duration time.Duration) ([]*output.DiskIOSpeed, *output.DiskIOSpeed, error) {
	var allSpeed output.DiskIOSpeed
	allSpeed.ReadSpeed = 0.0
	allSpeed.WriteSpeed = 0.0
	allSpeed.ReadOpsPerSec = 0.0
	allSpeed.WriteOpsPerSec = 0.0
	allSpeed.AvgIoLatency = 0.0

	speeds := make([]*output.DiskIOSpeed, len(oldStats))
	for _, oldStat := range oldStats {
		found := false
		for i, newStat := range newStats {
			if oldStat.Name == newStat.Name {
				found = true

				readSpeed := float64(newStat.ReadBytes-oldStat.ReadBytes) / duration.Seconds()
				writeSpeed := float64(newStat.WriteBytes-oldStat.WriteBytes) / duration.Seconds()

				readOpsPerSec := float64(newStat.ReadCount-oldStat.ReadCount) / duration.Seconds()
				writeOpsPerSec := float64(newStat.WriteCount-oldStat.WriteCount) / duration.Seconds()

				ioTimeDiff := float64(newStat.IoTime - oldStat.IoTime) // convert to ms
				ioOpsDiff := float64((newStat.ReadCount + newStat.WriteCount) - (oldStat.ReadCount + oldStat.WriteCount))
				avgIoLatency := 0.0
				if ioOpsDiff > 0 {
					avgIoLatency = ioTimeDiff / ioOpsDiff
				}

				allSpeed.ReadSpeed += readSpeed
				allSpeed.WriteSpeed += writeSpeed
				allSpeed.ReadOpsPerSec += readOpsPerSec
				allSpeed.WriteOpsPerSec += writeOpsPerSec
				allSpeed.AvgIoLatency += avgIoLatency

				speeds[i] = &output.DiskIOSpeed{
					Name:           oldStat.Name,
					ReadSpeed:      readSpeed,
					WriteSpeed:     writeSpeed,
					ReadOpsPerSec:  readOpsPerSec,
					WriteOpsPerSec: writeOpsPerSec,
					AvgIoLatency:   avgIoLatency,
				}
				break
			}
		}
		if !found {
			return nil, nil, fmt.Errorf("disk order changed")
		}
	}

	// Calculate average latency for all disks combined
	if len(speeds) > 0 {
		allSpeed.AvgIoLatency /= float64(len(speeds))
	}

	return speeds, &allSpeed, nil
}

func GetLibCount() (int64, error) {
	lib := models.Library{}
	var count int64
	tx := app.DB().Model(&lib).Count(&count)
	if tx.Error != nil {
		return 0, tx.Error
	}
	return count, nil
}

func GetWebSiteCount() (int64, error) {
	lib := models.Website{}
	var count int64
	tx := app.DB().Model(&lib).Count(&count)
	if tx.Error != nil {
		return 0, tx.Error
	}
	return count, nil
}

func SystemInfo() (map[string]interface{}, error) {
	port := app.ONE_CONFIG.System.Port
	u := models.User{}
	tx := app.DB().Model(&u).First(&u)
	if tx.Error != nil {
		return nil, tx.Error
	}
	s := models.System{}
	tx = app.DB().Model(&s).First(&s)
	if tx.Error != nil {
		return nil, tx.Error
	}
	info := map[string]interface{}{
		"port":  port,
		"user":  u,
		"title": s.Title,
	}
	return info, nil
}

func UpdateSystemPort(port string) error {
	if port == "" {
		return fmt.Errorf(" Port not provided")
	}
	configFile := app.GetBasePath() + "/config.yaml"
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		return fmt.Errorf(" Configuration file %s not found", configFile)
	}

	// 使用 viper 读取和更新配置
	v := viper.New()
	v.SetConfigFile(configFile)
	v.SetConfigType("yaml")

	// 读取配置文件
	err := v.ReadInConfig()
	if err != nil {
		return fmt.Errorf(" Failed to read configuration file: %v", err)
	}

	// 更新端口配置
	v.Set("system.port", port)

	// 保存更新到配置文件
	err = v.WriteConfig()
	if err != nil {
		return fmt.Errorf(" Failed to update configuration file: %v", err)
	}
	tx := app.DB().Model(&app.ONE_CONFIG.System).Update("port", port)
	if tx.Error != nil {
		return tx.Error
	}
	return nil
}

func UpdateUser(user models.User) error {
	u := models.User{}
	tx := app.DB().Where("username = ?", user.Username).First(&u)
	if tx.Error != nil {
		return tx.Error
	}
	if user.Username != "" && user.Password != "" {
		return fmt.Errorf("Username and Password cannot be empty")
	}
	if user.Password != "" {
		u.Password = user.Password
	}
	if user.Username != "" {
		u.Username = user.Username
	}
	tx = app.DB().Updates(u)
	if tx.Error != nil {
		return tx.Error
	}
	return nil
}

func ResetPassword(user input.ResetPasswordRequest) error {
	u := models.User{}
	tx := app.DB().Where("username = ? and password = ?", user.Username, user.Password).First(&u)
	if tx.Error != nil {
		return tx.Error
	}
	u.Password = user.NewPassword
	tx = app.DB().Updates(u)
	if tx.Error != nil {
		return tx.Error
	}
	return nil
}

func UpdateSystemTitle(title string) error {
	if title == "" {
		return fmt.Errorf(" Title not provided")
	}
	s := models.System{}
	tx := app.DB().Model(&s).First(&s)
	if tx.Error != nil {
		return tx.Error
	}
	s.Title = title
	tx = app.DB().Updates(s)
	if tx.Error != nil {
		return tx.Error
	}
	return nil
}

func GetInfo() (*models.System, error) {
	s := models.System{}
	tx := app.DB().Model(&s).First(&s)
	if tx.Error != nil {
		return nil, tx.Error
	}
	return &s, nil
}

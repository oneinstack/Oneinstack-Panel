package system

import (
	"errors"
	"fmt"
	"log"
	"oneinstack/app"
	"oneinstack/internal/crypto"
	"oneinstack/internal/models"
	"oneinstack/router/output"

	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"gorm.io/gorm"
)

var ErrCurrentPasswordInvalid = errors.New("current password is invalid")

// PasswordStrengthError 表示可安全返回给调用方的密码强度校验错误。
type PasswordStrengthError struct {
	message string
}

func (e *PasswordStrengthError) Error() string {
	return e.message
}

func newPasswordStrengthError(message string) error {
	return &PasswordStrengthError{message: message}
}

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
		return nil, fmt.Errorf("collect host information: %w", err)
	}
	// CPU Info
	cpuInfo, err := cpu.Info()
	if err != nil {
		return nil, fmt.Errorf("collect CPU information: %w", err)
	}
	// CPU Usage and capacity. CPU capacity is represented by logical cores so
	// that used/available values remain meaningful on hosts with SMT enabled.
	cpuPercent, err := cpu.Percent(time.Second, false)
	if err != nil {
		return nil, fmt.Errorf("collect CPU usage: %w", err)
	}
	if len(cpuPercent) == 0 {
		return nil, fmt.Errorf("cpu usage is unavailable")
	}
	logicalCores, err := cpu.Counts(true)
	if err != nil {
		return nil, fmt.Errorf("collect logical CPU count: %w", err)
	}
	physicalCores, err := cpu.Counts(false)
	if err != nil {
		return nil, fmt.Errorf("collect physical CPU count: %w", err)
	}
	usedPercent := cpuPercent[0]
	if usedPercent < 0 {
		usedPercent = 0
	}
	if usedPercent > 100 {
		usedPercent = 100
	}
	totalCores := float64(logicalCores)
	usedCores := totalCores * usedPercent / 100

	// Memory Info
	vmem, err := mem.VirtualMemory()
	if err != nil {
		return nil, fmt.Errorf("collect memory information: %w", err)
	}
	// Disk Info
	partitions, err := disk.Partitions(true)
	if err != nil {
		return nil, fmt.Errorf("list disk partitions: %w", err)
	}
	var diskUsages []disk.UsageStat
	for _, partition := range partitions {
		usage, err := disk.Usage(partition.Mountpoint)
		if err != nil {
			// The mount list and the filesystem state can change between the
			// two calls. A stale or inaccessible optional mount must not make
			// the whole system-info endpoint unavailable.
			log.Printf("system info: skip disk usage for mountpoint %q: %v", partition.Mountpoint, err)
			continue
		}
		if usage == nil {
			log.Printf("system info: skip disk usage for mountpoint %q: empty result", partition.Mountpoint)
			continue
		}
		diskUsages = append(diskUsages, *usage)
	}

	// Network Info
	interfaces, err := net.IOCounters(true)
	if err != nil {
		return nil, fmt.Errorf("collect network information: %w", err)
	}
	sysinfo := &output.SystemInfo{
		HostInfo: info,
		CPUInfo:  cpuInfo,
		CPU:      cpuPercent,
		CPUSummary: &output.CPUSummary{
			Total:         totalCores,
			Used:          usedCores,
			Available:     totalCores - usedCores,
			UsedPercent:   usedPercent,
			LogicalCores:  logicalCores,
			PhysicalCores: physicalCores,
		},
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

// 获取备忘录数量
func GetRemarkCount() (int64, error) {
	lib := models.Remark{}
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

func SystemInfo() (*output.PanelSettings, error) {
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
	info := &output.PanelSettings{
		Port: port,
		User: output.UserSummary{
			ID:         u.ID,
			Username:   u.Username,
			IsAdmin:    u.IsAdmin,
			FirstJoin:  u.FirstJoin,
			CreateTime: u.CreateTime,
		},
		Title: s.Title,
	}
	return info, nil
}

func UpdateSystemPort(port string) error {
	current, err := loadStoredPanelConfig()
	if err != nil {
		return err
	}
	_, err = UpdatePanelNetwork(UpdatePanelNetworkRequest{
		BindAddress:          current.BindAddress,
		HTTPPort:             strings.TrimSpace(port),
		HTTPSEnabled:         current.HTTPSEnabled,
		HTTPSPort:            current.HTTPSPort,
		HTTPSCertificateFile: current.CertificateFile,
		HTTPSPrivateKeyFile:  current.PrivateKeyFile,
		TrustedProxies:       current.TrustedProxies,
	})
	return err
}

func UpdateUser(userID int64, username string) error {
	u := models.User{}
	tx := app.DB().Where("id = ?", userID).First(&u)
	if tx.Error != nil {
		return tx.Error
	}
	if username == "" {
		return fmt.Errorf("Username is empty")
	}
	u.Username = username
	tx = app.DB().Updates(u)
	if tx.Error != nil {
		return tx.Error
	}
	return nil
}

func ResetPassword(userID int64, currentPassword, newPassword string) error {
	// 验证密码强度
	if err := validatePasswordStrength(newPassword); err != nil {
		return err
	}

	// 加密密码
	hashedPassword, err := crypto.HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("failed to hash password: %v", err)
	}
	return app.DB().Transaction(func(tx *gorm.DB) error {
		var u models.User
		if err := tx.Where("id = ?", userID).First(&u).Error; err != nil {
			return err
		}
		if !u.MustChangePassword && !crypto.CheckPasswordHash(currentPassword, u.Password) {
			return ErrCurrentPasswordInvalid
		}
		nextVersion := u.EffectiveSecurityVersion() + 1
		if err := tx.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]interface{}{
			"password":               hashedPassword,
			"must_change_password":   false,
			"password_change_reason": "",
			"security_version":       nextVersion,
		}).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		return tx.Model(&models.UserSession{}).
			Where("user_id = ? AND revoked_at IS NULL", userID).
			Updates(map[string]interface{}{
				"revoked_at": now, "revocation_reason": "password_changed",
			}).Error
	})
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

// validatePasswordStrength 验证密码强度
func validatePasswordStrength(password string) error {
	if len(password) < 8 {
		return newPasswordStrengthError("密码长度不能少于 8 个字符")
	}

	if len(password) > 128 {
		return newPasswordStrengthError("密码长度不能超过 128 个字符")
	}

	var (
		hasUpper   = false
		hasLower   = false
		hasNumber  = false
		hasSpecial = false
	)

	for _, char := range password {
		switch {
		case unicode.IsUpper(char):
			hasUpper = true
		case unicode.IsLower(char):
			hasLower = true
		case unicode.IsNumber(char):
			hasNumber = true
		case unicode.IsPunct(char) || unicode.IsSymbol(char):
			hasSpecial = true
		}
	}

	if !hasUpper {
		return newPasswordStrengthError("密码必须包含至少一个大写字母")
	}
	if !hasLower {
		return newPasswordStrengthError("密码必须包含至少一个小写字母")
	}
	if !hasNumber {
		return newPasswordStrengthError("密码必须包含至少一个数字")
	}
	if !hasSpecial {
		return newPasswordStrengthError("密码必须包含至少一个特殊字符")
	}

	// 检查常见弱密码
	commonPasswords := []string{
		"password", "123456", "123456789", "12345678", "12345", "1234567",
		"admin", "administrator", "root", "user", "test", "guest",
	}

	for _, common := range commonPasswords {
		if matched, _ := regexp.MatchString("(?i)"+common, password); matched {
			return newPasswordStrengthError("密码不能包含 123456、password、admin 等常见弱密码片段")
		}
	}

	return nil
}

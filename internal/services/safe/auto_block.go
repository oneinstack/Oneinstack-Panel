package safe

import (
	"context"
	"errors"
	"log"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"oneinstack/internal/models"
	"oneinstack/router/input"
)

const (
	defaultAutoBlockThreshold = 8
	defaultAutoBlockWindow    = 10
	defaultAutoBlockBan       = 1440
)

var failedSSHAddressPattern = regexp.MustCompile(`(?i)(?:failed password|authentication failure).*?(?:from|rhost=)\s*([0-9]{1,3}(?:\.[0-9]{1,3}){3})`)

func (s *Service) GetAutoBlockConfig() (*models.FirewallAutoBlockConfig, error) {
	config := &models.FirewallAutoBlockConfig{ID: 1}
	result := s.db.First(config, 1)
	if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, result.Error
	}
	applyAutoBlockDefaults(config)
	return config, nil
}

func (s *Service) SaveAutoBlockConfig(param *input.FirewallAutoBlockParam) (*models.FirewallAutoBlockConfig, error) {
	if param == nil {
		return nil, validationError("自动封禁配置不能为空")
	}
	config := &models.FirewallAutoBlockConfig{
		ID: 1, Enabled: param.Enabled, Threshold: param.Threshold,
		WindowMinutes: param.WindowMinutes, BanMinutes: param.BanMinutes,
	}
	applyAutoBlockDefaults(config)
	if config.Threshold < 3 || config.Threshold > 100 {
		return nil, validationError("触发次数必须在 3-100 之间")
	}
	if config.WindowMinutes < 1 || config.WindowMinutes > 1440 {
		return nil, validationError("统计周期必须在 1-1440 分钟之间")
	}
	if config.BanMinutes < 5 || config.BanMinutes > 525600 {
		return nil, validationError("封禁时间必须在 5-525600 分钟之间")
	}
	if err := s.db.Save(config).Error; err != nil {
		return nil, err
	}
	return config, nil
}

func (s *Service) RunAutoBlock(ctx context.Context) (int, error) {
	config, err := s.GetAutoBlockConfig()
	if err != nil || !config.Enabled {
		return 0, err
	}
	if _, err := s.runner.LookPath("journalctl"); err != nil {
		return 0, nil
	}
	since := strconv.Itoa(config.WindowMinutes) + " minutes ago"
	output, err := s.runner.Run(ctx, "journalctl", "--no-pager", "--since", since, "-u", "ssh", "-u", "sshd")
	if err != nil {
		return 0, err
	}
	counts := make(map[string]int)
	for _, match := range failedSSHAddressPattern.FindAllStringSubmatch(string(output), -1) {
		if len(match) < 2 {
			continue
		}
		ip := net.ParseIP(match[1])
		if ip == nil || ip.To4() == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		counts[ip.To4().String()]++
	}
	now := time.Now()
	expires := now.Add(time.Duration(config.BanMinutes) * time.Minute)
	created := 0
	for address, count := range counts {
		if count < config.Threshold {
			continue
		}
		var existing int64
		if err := s.db.Model(&models.IptablesRule{}).
			Where("rule_type = ? AND ips = ? AND state = ? AND (expires_at IS NULL OR expires_at > ?)",
				"auto_block", address, 1, now).
			Count(&existing).Error; err != nil {
			return created, err
		}
		if existing > 0 {
			continue
		}
		rule := &models.IptablesRule{
			RuleType: "auto_block", Direction: "in", Protocol: "all",
			Strategy: "deny", IPs: address, State: 1,
			Remark: "SSH 失败登录自动封禁", Location: describeIPLocation(address),
			ExpiresAt: &expires,
		}
		if err := s.Add(ctx, rule); err != nil {
			return created, err
		}
		created++
		if created >= 100 {
			break
		}
	}
	current := time.Now()
	if err := s.db.Model(&models.FirewallAutoBlockConfig{}).
		Where("id = ?", 1).Update("last_run_at", &current).Error; err != nil {
		return created, err
	}
	return created, nil
}

// RunMaintenance removes expired temporary blocks and evaluates the opt-in SSH
// auto-block policy. It stops with ctx and intentionally skips overlapping runs.
func RunMaintenance(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	run := func() {
		service := NewDefaultService()
		if cleaned, err := service.CleanupExpired(ctx); err != nil {
			log.Printf("firewall expired-rule cleanup failed: %v", err)
		} else if cleaned > 0 {
			log.Printf("firewall expired-rule cleanup removed %d rules", cleaned)
		}
		if blocked, err := service.RunAutoBlock(ctx); err != nil {
			log.Printf("firewall automatic block scan failed: %v", err)
		} else if blocked > 0 {
			log.Printf("firewall automatic block scan added %d rules", blocked)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func applyAutoBlockDefaults(config *models.FirewallAutoBlockConfig) {
	if config.Threshold == 0 {
		config.Threshold = defaultAutoBlockThreshold
	}
	if config.WindowMinutes == 0 {
		config.WindowMinutes = defaultAutoBlockWindow
	}
	if config.BanMinutes == 0 {
		config.BanMinutes = defaultAutoBlockBan
	}
}

func parseAutoBlockExpiresAt(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, validationError("过期时间格式必须是 RFC3339")
	}
	return &value, nil
}

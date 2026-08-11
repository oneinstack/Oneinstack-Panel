package fail2ban

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	managedConfigDir = "/etc/fail2ban/jail.d"
	manualLogPath    = "/var/lib/oneinstack/fail2ban/manual.log"
	eventFilePath    = "/var/lib/oneinstack/fail2ban/events.jsonl"
)

var (
	ErrValidation       = errors.New("invalid fail2ban request")
	ErrRevisionConflict = errors.New("fail2ban policy revision conflict")
	ErrProtectedAddress = errors.New("protected address cannot be banned")
	ErrUnavailable      = errors.New("fail2ban service is unavailable")
)

type Template struct {
	Key               string `json:"key"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	DefaultMaxRetry   int    `json:"defaultMaxRetry"`
	DefaultFindTime   int    `json:"defaultFindTimeSeconds"`
	DefaultBanTime    int    `json:"defaultBanTimeSeconds"`
	ProtectedPorts    string `json:"protectedPorts"`
	SupportsDetection bool   `json:"supportsDetection"`
}

type PolicyInput struct {
	ID              string   `json:"id,omitempty"`
	BaseRevision    string   `json:"baseRevision,omitempty"`
	Template        string   `json:"template,omitempty"`
	Name            string   `json:"name,omitempty"`
	Enabled         bool     `json:"enabled"`
	EnforcementMode string   `json:"enforcementMode,omitempty"`
	MaxRetry        int      `json:"maxRetry,omitempty"`
	FindTimeSeconds int      `json:"findTimeSeconds,omitempty"`
	BanTimeSeconds  int      `json:"banTimeSeconds,omitempty"`
	IgnoreIPs       []string `json:"ignoreIps,omitempty"`
}

type PolicyChangeRequest struct {
	Action    string      `json:"action"`
	Policy    PolicyInput `json:"policy"`
	RequestIP string      `json:"-"`
}

type BanRequest struct {
	IncidentID string `json:"incidentId,omitempty"`
	PolicyID   string `json:"policyId"`
	IP         string `json:"ip,omitempty"`
	Reason     string `json:"reason"`
	RequestIP  string `json:"-"`
}

type Status struct {
	Installed       bool                 `json:"installed"`
	ServiceActive   bool                 `json:"serviceActive"`
	Version         string               `json:"version,omitempty"`
	Jails           []string             `json:"jails"`
	ManagedPolicies int                  `json:"managedPolicies"`
	ActiveBans      int                  `json:"activeBans"`
	Migration       models.Fail2banState `json:"migration"`
	Warning         string               `json:"warning,omitempty"`
}

type PolicyView struct {
	models.Fail2banPolicy
	ActualEnabled     bool     `json:"actualEnabled"`
	Drifted           bool     `json:"drifted"`
	EffectiveIgnoreIP []string `json:"effectiveIgnoreIps"`
}

type Ban struct {
	PolicyID string `json:"policyId"`
	Policy   string `json:"policy"`
	Jail     string `json:"jail"`
	IP       string `json:"ip"`
	Managed  bool   `json:"managed"`
	BanTime  int    `json:"banTimeSeconds"`
}

type Service struct {
	db *gorm.DB
}

func NewService(database *gorm.DB) *Service { return &Service{db: database} }

func DefaultService() *Service { return NewService(app.DB()) }

func Templates() []Template {
	port := strings.TrimSpace(app.ONE_CONFIG.System.Port)
	if port == "" {
		port = "8089"
	}
	return []Template{
		{Key: "sshd", Name: "SSH 登录防护", Description: "检测 SSH 密码和认证失败事件", DefaultMaxRetry: 8, DefaultFindTime: 600, DefaultBanTime: 86400, ProtectedPorts: "ssh", SupportsDetection: true},
		{Key: "panel-login", Name: "Panel 登录防护", Description: "根据 Panel 安全审计中的连续登录失败生成事件", DefaultMaxRetry: 8, DefaultFindTime: 600, DefaultBanTime: 86400, ProtectedPorts: port, SupportsDetection: true},
		{Key: "nginx-http-auth", Name: "Nginx HTTP 认证防护", Description: "检测 Nginx HTTP 基础认证失败", DefaultMaxRetry: 5, DefaultFindTime: 600, DefaultBanTime: 3600, ProtectedPorts: "http,https", SupportsDetection: true},
		{Key: "nginx-botsearch", Name: "Nginx 恶意扫描防护", Description: "检测针对常见敏感路径的恶意扫描", DefaultMaxRetry: 5, DefaultFindTime: 600, DefaultBanTime: 3600, ProtectedPorts: "http,https", SupportsDetection: true},
	}
}

func templateByKey(key string) (Template, bool) {
	for _, item := range Templates() {
		if item.Key == key {
			return item, true
		}
	}
	return Template{}, false
}

func (s *Service) NormalizePolicyChange(request PolicyChangeRequest, userID int64) (PolicyChangeRequest, *models.Fail2banPolicy, error) {
	if s == nil || s.db == nil {
		return request, nil, ErrUnavailable
	}
	request.Action = strings.ToLower(strings.TrimSpace(request.Action))
	if request.Action != "create" && request.Action != "update" && request.Action != "delete" {
		return request, nil, validation("action 必须是 create、update 或 delete")
	}
	if userID < 0 {
		return request, nil, validation("无法识别规则操作者")
	}
	var existing models.Fail2banPolicy
	if request.Action != "create" {
		if _, err := uuid.Parse(request.Policy.ID); err != nil {
			return request, nil, validation("规则 ID 无效")
		}
		if err := s.db.First(&existing, "id = ?", request.Policy.ID).Error; err != nil {
			return request, nil, err
		}
		if request.Policy.BaseRevision == "" || request.Policy.BaseRevision != existing.Revision {
			return request, &existing, ErrRevisionConflict
		}
		if request.Action == "delete" {
			return request, &existing, nil
		}
	}

	input := request.Policy
	if request.Action == "create" {
		input.ID = uuid.NewString()
	} else {
		input.ID = existing.ID
	}
	input.Template = strings.ToLower(strings.TrimSpace(input.Template))
	template, ok := templateByKey(input.Template)
	if !ok {
		return request, nil, validation("规则模板只支持 sshd、panel-login、nginx-http-auth 或 nginx-botsearch")
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len([]rune(input.Name)) > 64 {
		return request, nil, validation("规则名称长度必须为 1-64 个字符")
	}
	input.EnforcementMode = strings.TrimSpace(input.EnforcementMode)
	if input.EnforcementMode == "" {
		input.EnforcementMode = "observe"
	}
	if input.EnforcementMode != "observe" && input.EnforcementMode != "autoBan" {
		return request, nil, validation("处置模式必须是 observe 或 autoBan")
	}
	if input.MaxRetry == 0 {
		input.MaxRetry = template.DefaultMaxRetry
	}
	if input.FindTimeSeconds == 0 {
		input.FindTimeSeconds = template.DefaultFindTime
	}
	if input.BanTimeSeconds == 0 {
		input.BanTimeSeconds = template.DefaultBanTime
	}
	if input.MaxRetry < 3 || input.MaxRetry > 100 {
		return request, nil, validation("触发次数必须在 3-100 之间")
	}
	if input.FindTimeSeconds < 60 || input.FindTimeSeconds > 86400 {
		return request, nil, validation("统计窗口必须在 60-86400 秒之间")
	}
	if input.BanTimeSeconds < 300 || input.BanTimeSeconds > 31536000 {
		return request, nil, validation("封禁时间必须在 300-31536000 秒之间")
	}
	ignore, err := normalizeNetworks(input.IgnoreIPs)
	if err != nil {
		return request, nil, err
	}
	input.IgnoreIPs = ignore
	request.Policy = input

	suffix := strings.ReplaceAll(input.ID, "-", "")[:12]
	policy := &models.Fail2banPolicy{
		ID: input.ID, Template: input.Template, Name: input.Name,
		Enabled: input.Enabled, EnforcementMode: input.EnforcementMode,
		MaxRetry: input.MaxRetry, FindTimeSeconds: input.FindTimeSeconds,
		BanTimeSeconds: input.BanTimeSeconds, IgnoreIPs: ignore,
		JailName:  "oneinstack-" + suffix,
		CreatedBy: userID, UpdatedBy: userID,
	}
	if input.Template != "panel-login" {
		policy.DetectorJail = policy.JailName + "-detect"
	}
	if request.Action == "update" {
		policy.CreatedAt = existing.CreatedAt
		policy.CreatedBy = existing.CreatedBy
	}
	policy.Revision = policyRevision(policy)
	return request, policy, nil
}

func (s *Service) ApplyPolicyChange(ctx context.Context, request PolicyChangeRequest, userID int64) (*models.Fail2banPolicy, error) {
	request, policy, err := s.NormalizePolicyChange(request, userID)
	if err != nil {
		return nil, err
	}
	status, err := s.Status(ctx)
	if err != nil || !status.Installed || !status.ServiceActive {
		return nil, ErrUnavailable
	}
	path := managedPolicyPath(policy.ID)
	old, oldErr := os.ReadFile(path)
	if oldErr != nil && !errors.Is(oldErr, os.ErrNotExist) {
		return nil, oldErr
	}
	if request.Action == "delete" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err := s.validateAndReload(ctx); err != nil {
			_ = restoreFile(path, old, oldErr == nil)
			return nil, err
		}
		if err := s.db.Delete(&models.Fail2banPolicy{}, "id = ?", policy.ID).Error; err != nil {
			_ = restoreFile(path, old, oldErr == nil)
			_ = s.validateAndReload(ctx)
			return nil, err
		}
		return policy, nil
	}

	contents, err := s.renderPolicy(policy)
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(path, contents, 0640); err != nil {
		return nil, err
	}
	if err := s.validateAndReload(ctx); err != nil {
		_ = restoreFile(path, old, oldErr == nil)
		_ = s.validateAndReload(ctx)
		return nil, err
	}
	now := time.Now().UTC()
	policy.LastAppliedAt = &now
	policy.LastApplyError = ""
	if err := s.db.Save(policy).Error; err != nil {
		_ = restoreFile(path, old, oldErr == nil)
		_ = s.validateAndReload(ctx)
		return nil, err
	}
	return policy, nil
}

func (s *Service) renderPolicy(policy *models.Fail2banPolicy) ([]byte, error) {
	template, ok := templateByKey(policy.Template)
	if !ok {
		return nil, validation("规则模板不可用")
	}
	ignore := effectiveIgnoreIPs(policy.IgnoreIPs)
	enabled := strconv.FormatBool(policy.Enabled)
	var builder strings.Builder
	fmt.Fprintf(&builder, "# Managed by OneinStack Panel. Do not edit.\n# revision=%s\n\n", policy.Revision)
	if policy.DetectorJail != "" {
		fmt.Fprintf(&builder, "[%s]\nenabled = %s\nfilter = %s\n", policy.DetectorJail, enabled, policy.Template)
		switch policy.Template {
		case "sshd":
			builder.WriteString("backend = systemd\n")
		case "nginx-http-auth", "nginx-botsearch":
			path := nginxLogPath()
			if policy.Enabled {
				if _, err := os.Stat(path); err != nil {
					return nil, fmt.Errorf("Nginx 日志不可用，无法启用该规则: %w", err)
				}
			}
			fmt.Fprintf(&builder, "logpath = %s\n", path)
		}
		fmt.Fprintf(&builder, "port = %s\nmaxretry = %d\nfindtime = %d\nbantime = 1\nignoreip = %s\naction = oneinstack-report\n\n",
			template.ProtectedPorts, policy.MaxRetry, policy.FindTimeSeconds, strings.Join(ignore, " "))
	}
	fmt.Fprintf(&builder, "[%s]\nenabled = %s\nfilter = oneinstack-manual\nlogpath = %s\nport = %s\nmaxretry = 999999\nfindtime = %d\nbantime = %d\nignoreip = %s\naction = %%(action_)s\n",
		policy.JailName, enabled, manualLogPath, template.ProtectedPorts,
		policy.FindTimeSeconds, policy.BanTimeSeconds, strings.Join(ignore, " "))
	return []byte(builder.String()), nil
}

func (s *Service) validateAndReload(ctx context.Context) error {
	if _, err := run(ctx, "-t"); err != nil {
		return fmt.Errorf("Fail2ban 配置校验失败: %w", err)
	}
	if _, err := run(ctx, "reload"); err != nil {
		return fmt.Errorf("Fail2ban 重载失败: %w", err)
	}
	return nil
}

func (s *Service) Status(ctx context.Context) (*Status, error) {
	result := &Status{Jails: []string{}}
	if s == nil || s.db == nil {
		return nil, ErrUnavailable
	}
	_ = s.db.FirstOrCreate(&result.Migration, models.Fail2banState{ID: 1, MigrationStatus: "pending"}).Error
	var managedPolicies int64
	_ = s.db.Model(&models.Fail2banPolicy{}).Count(&managedPolicies).Error
	result.ManagedPolicies = int(managedPolicies)
	if _, err := exec.LookPath("fail2ban-client"); err != nil {
		result.Warning = "服务器尚未安装 Fail2ban 组件"
		return result, nil
	}
	result.Installed = true
	if version, err := run(ctx, "--version"); err == nil {
		result.Version = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(version), "Fail2Ban v"))
	}
	if output, err := run(ctx, "ping"); err != nil || !strings.Contains(strings.ToLower(output), "pong") {
		result.Warning = "Fail2ban 服务未运行或无法响应"
		return result, nil
	}
	result.ServiceActive = true
	if output, err := run(ctx, "status"); err == nil {
		result.Jails = parseJailList(output)
	}
	bans, _ := s.ListBans(ctx)
	result.ActiveBans = len(bans)
	return result, nil
}

func (s *Service) ListPolicies(ctx context.Context) ([]PolicyView, error) {
	var policies []models.Fail2banPolicy
	if err := s.db.Order("created_at ASC").Find(&policies).Error; err != nil {
		return nil, err
	}
	status, _ := s.Status(ctx)
	active := make(map[string]struct{})
	if status != nil {
		for _, jail := range status.Jails {
			active[jail] = struct{}{}
		}
	}
	result := make([]PolicyView, 0, len(policies))
	for i := range policies {
		contents, renderErr := s.renderPolicy(&policies[i])
		stored, readErr := os.ReadFile(managedPolicyPath(policies[i].ID))
		_, actual := active[policies[i].JailName]
		result = append(result, PolicyView{
			Fail2banPolicy: policies[i], ActualEnabled: actual,
			Drifted:           renderErr != nil || readErr != nil || string(stored) != string(contents),
			EffectiveIgnoreIP: effectiveIgnoreIPs(policies[i].IgnoreIPs),
		})
	}
	return result, nil
}

func (s *Service) ListBans(ctx context.Context) ([]Ban, error) {
	var policies []models.Fail2banPolicy
	if err := s.db.Where("enabled = ?", true).Order("created_at ASC").Find(&policies).Error; err != nil {
		return nil, err
	}
	result := make([]Ban, 0)
	for _, policy := range policies {
		output, err := run(ctx, "status", policy.JailName)
		if err != nil {
			continue
		}
		for _, address := range parseBannedIPs(output) {
			result = append(result, Ban{PolicyID: policy.ID, Policy: policy.Name, Jail: policy.JailName, IP: address, Managed: true, BanTime: policy.BanTimeSeconds})
		}
	}
	return result, nil
}

func (s *Service) ResolveBanRequest(request BanRequest) (BanRequest, *models.Fail2banPolicy, *models.SecurityIncident, error) {
	request.PolicyID = strings.TrimSpace(request.PolicyID)
	request.IncidentID = strings.TrimSpace(request.IncidentID)
	request.Reason = strings.TrimSpace(request.Reason)
	if len([]rune(request.Reason)) < 2 || len([]rune(request.Reason)) > 200 {
		return request, nil, nil, validation("封禁或解封原因长度必须为 2-200 个字符")
	}
	var policy models.Fail2banPolicy
	if err := s.db.First(&policy, "id = ?", request.PolicyID).Error; err != nil {
		return request, nil, nil, err
	}
	if !policy.Enabled {
		return request, nil, nil, validation("目标规则尚未启用")
	}
	var incident *models.SecurityIncident
	if request.IncidentID != "" {
		var record models.SecurityIncident
		if err := s.db.First(&record, "id = ? AND policy_id = ?", request.IncidentID, policy.ID).Error; err != nil {
			return request, nil, nil, err
		}
		request.IP = record.RemoteIP
		incident = &record
	}
	ip := net.ParseIP(strings.TrimSpace(request.IP))
	if ip == nil || strings.Contains(request.IP, "/") {
		return request, nil, nil, validation("封禁目标必须是单个有效 IP 地址")
	}
	request.IP = ip.String()
	if isProtectedIP(ip, request.RequestIP) {
		return request, nil, nil, ErrProtectedAddress
	}
	return request, &policy, incident, nil
}

func (s *Service) Ban(ctx context.Context, request BanRequest, taskID string) error {
	request, policy, incident, err := s.ResolveBanRequest(request)
	if err != nil {
		return err
	}
	if _, err := run(ctx, "set", policy.JailName, "banip", request.IP); err != nil {
		bans, _ := s.ListBans(ctx)
		if !containsBan(bans, policy.ID, request.IP) {
			return err
		}
	}
	if incident != nil {
		now := time.Now().UTC()
		_ = s.db.Model(&models.SecurityIncident{}).Where("id = ?", incident.ID).Updates(map[string]any{
			"status": "blocked", "task_id": taskID, "resolved_at": &now, "updated_at": now,
		}).Error
	}
	return nil
}

func (s *Service) Unban(ctx context.Context, request BanRequest) error {
	request, policy, _, err := s.ResolveBanRequest(request)
	if err != nil {
		return err
	}
	if _, err := run(ctx, "set", policy.JailName, "unbanip", request.IP); err != nil {
		return err
	}
	return nil
}

func (s *Service) DismissIncident(id string, userID int64) error {
	parsed, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil || parsed == uuid.Nil {
		return validation("异常事件 ID 无效")
	}
	now := time.Now().UTC()
	result := s.db.Model(&models.SecurityIncident{}).
		Where("id = ? AND status = ?", id, "open").
		Updates(map[string]any{"status": "dismissed", "resolved_at": &now, "resolved_by": userID, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func run(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "fail2ban-client", args...)
	output, err := command.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		return text, fmt.Errorf("fail2ban-client %s failed", strings.Join(args, " "))
	}
	return text, nil
}

func validation(message string) error { return fmt.Errorf("%w: %s", ErrValidation, message) }

func policyRevision(policy *models.Fail2banPolicy) string {
	value := struct {
		Template string
		Name     string
		Enabled  bool
		Mode     string
		MaxRetry int
		FindTime int
		BanTime  int
		Ignore   []string
	}{policy.Template, policy.Name, policy.Enabled, policy.EnforcementMode, policy.MaxRetry, policy.FindTimeSeconds, policy.BanTimeSeconds, policy.IgnoreIPs}
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func normalizeNetworks(values []string) ([]string, error) {
	if len(values) > 64 {
		return nil, validation("自定义白名单最多包含 64 个地址或网段")
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		var normalized string
		if strings.Contains(value, "/") {
			_, network, err := net.ParseCIDR(value)
			if err != nil {
				return nil, validation("白名单包含无效的 IP 网段")
			}
			normalized = network.String()
		} else {
			ip := net.ParseIP(value)
			if ip == nil {
				return nil, validation("白名单包含无效的 IP 地址")
			}
			normalized = ip.String()
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	sort.Strings(result)
	return result, nil
}

func effectiveIgnoreIPs(custom []string) []string {
	values := append([]string{
		"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
		"::1", "fc00::/7", "fe80::/10",
	}, custom...)
	values = append(values, app.ONE_CONFIG.System.TrustedProxies...)
	if ip := net.ParseIP(strings.TrimSpace(app.ONE_CONFIG.System.BindAddress)); ip != nil && !ip.IsUnspecified() {
		values = append(values, ip.String())
	}
	result, _ := normalizeNetworks(values)
	return result
}

func isProtectedIP(ip net.IP, requestIP string) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return true
	}
	if current := net.ParseIP(strings.TrimSpace(requestIP)); current != nil && current.Equal(ip) {
		return true
	}
	for _, value := range app.ONE_CONFIG.System.TrustedProxies {
		if networkContains(value, ip) {
			return true
		}
	}
	if bind := net.ParseIP(strings.TrimSpace(app.ONE_CONFIG.System.BindAddress)); bind != nil && bind.Equal(ip) {
		return true
	}
	return false
}

func networkContains(value string, ip net.IP) bool {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "/") {
		_, network, err := net.ParseCIDR(value)
		return err == nil && network.Contains(ip)
	}
	parsed := net.ParseIP(value)
	return parsed != nil && parsed.Equal(ip)
}

func managedPolicyPath(id string) string {
	suffix := strings.ReplaceAll(id, "-", "")
	return filepath.Join(managedConfigDir, "90-oneinstack-"+suffix+".local")
}

func writeAtomic(path string, contents []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".oneinstack-fail2ban-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func restoreFile(path string, contents []byte, existed bool) error {
	if !existed {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeAtomic(path, contents, 0640)
}

func nginxLogPath() string {
	for _, path := range []string{"/var/log/nginx/error.log", "/usr/local/openresty/nginx/logs/error.log", "/usr/local/nginx/logs/error.log"} {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return "/var/log/nginx/error.log"
}

func parseJailList(output string) []string {
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "Jail list:") {
			continue
		}
		parts := strings.SplitN(line, "Jail list:", 2)
		if len(parts) != 2 {
			break
		}
		result := make([]string, 0)
		for _, value := range strings.Split(parts[1], ",") {
			if value = strings.TrimSpace(value); value != "" {
				result = append(result, value)
			}
		}
		sort.Strings(result)
		return result
	}
	return []string{}
}

func parseBannedIPs(output string) []string {
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "Banned IP list:") {
			continue
		}
		parts := strings.SplitN(line, "Banned IP list:", 2)
		if len(parts) != 2 {
			return nil
		}
		return strings.Fields(parts[1])
	}
	return nil
}

func containsBan(values []Ban, policyID, address string) bool {
	for _, value := range values {
		if value.PolicyID == policyID && value.IP == address {
			return true
		}
	}
	return false
}

func eventSpoolPath() string { return eventFilePath }

package fail2ban

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"oneinstack/app"
	"oneinstack/internal/models"
	auditservice "oneinstack/internal/services/audit"

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
	ErrConfigValidation = errors.New("Fail2ban 配置校验失败")
	ErrReload           = errors.New("Fail2ban 重载失败")
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
	IncidentID     string `json:"incidentId,omitempty"`
	PolicyID       string `json:"policyId"`
	IP             string `json:"ip,omitempty"`
	Reason         string `json:"reason"`
	BanTimeSeconds int    `json:"banTimeSeconds,omitempty"`
	BanMinutes     int    `json:"banMinutes,omitempty"`
	RequestIP      string `json:"-"`
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
	PolicyID  string     `json:"policyId"`
	Policy    string     `json:"policy"`
	Jail      string     `json:"jail"`
	IP        string     `json:"ip"`
	Managed   bool       `json:"managed"`
	BanTime   int        `json:"banTimeSeconds"`
	CreatedAt *time.Time `json:"createdAt,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

type Service struct {
	db    *gorm.DB
	banMu sync.Mutex
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
		{Key: "mysql-auth", Name: "MySQL 登录防护", Description: "检测 MySQL 登录认证失败", DefaultMaxRetry: 5, DefaultFindTime: 600, DefaultBanTime: 3600, ProtectedPorts: "3306", SupportsDetection: true},
		{Key: "redis-auth", Name: "Redis 认证防护", Description: "检测 Redis 认证失败和未授权访问", DefaultMaxRetry: 5, DefaultFindTime: 600, DefaultBanTime: 3600, ProtectedPorts: "6379", SupportsDetection: true},
		{Key: "vsftpd-auth", Name: "FTP 登录防护", Description: "检测 vsftpd FTP 登录失败", DefaultMaxRetry: 5, DefaultFindTime: 600, DefaultBanTime: 3600, ProtectedPorts: "21", SupportsDetection: true},
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
	return s.normalizePolicyChange(request, userID, false)
}

// NormalizePolicyChangeAction maps legacy client terminology to the canonical
// policy-change actions used by the service and task executor.
func NormalizePolicyChangeAction(action string) (string, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "edit" {
		action = "update"
	}
	if action != "create" && action != "update" && action != "delete" {
		return "", validation("action 必须是 create、update 或 delete")
	}
	return action, nil
}

func (s *Service) normalizePolicyChange(request PolicyChangeRequest, userID int64, preserveCreateID bool) (PolicyChangeRequest, *models.Fail2banPolicy, error) {
	if s == nil || s.db == nil {
		return request, nil, ErrUnavailable
	}
	action, err := NormalizePolicyChangeAction(request.Action)
	if err != nil {
		return request, nil, err
	}
	request.Action = action
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
		if preserveCreateID {
			parsed, err := uuid.Parse(strings.TrimSpace(input.ID))
			if err != nil || parsed == uuid.Nil {
				return request, nil, validation("规则 ID 无效")
			}
			input.ID = parsed.String()
		} else {
			input.ID = uuid.NewString()
		}
	} else {
		input.ID = existing.ID
	}
	input.Template = strings.ToLower(strings.TrimSpace(input.Template))
	template, ok := templateByKey(input.Template)
	if !ok {
		return request, nil, validation("规则模板只支持 sshd、panel-login、nginx-http-auth、nginx-botsearch、mysql-auth、redis-auth 或 vsftpd-auth")
	}
	if request.Action == "create" {
		var sameTemplateCount int64
		if err := s.db.Model(&models.Fail2banPolicy{}).Where("template = ?", input.Template).Count(&sameTemplateCount).Error; err != nil {
			return request, nil, err
		}
		if sameTemplateCount > 0 {
			return request, nil, validation(fmt.Sprintf("策略模板“%s”（%s）已存在，请直接编辑现有策略，不要重复添加。", template.Name, input.Template))
		}
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
	preserveCreateID := strings.EqualFold(strings.TrimSpace(request.Action), "create") && strings.TrimSpace(request.Policy.ID) != ""
	request, policy, err := s.normalizePolicyChange(request, userID, preserveCreateID)
	if err != nil {
		return nil, err
	}
	status, err := s.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if status == nil || !status.Installed || !status.ServiceActive {
		reason := "Fail2ban 未安装、未验证或服务不可用"
		if status != nil && strings.TrimSpace(status.Warning) != "" {
			reason = strings.TrimSpace(status.Warning)
		}
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, reason)
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
		if err := s.db.Delete(&models.Fail2banBan{}, "policy_id = ?", policy.ID).Error; err != nil {
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
	filterName := templateFilterName(policy.Template)
	var builder strings.Builder
	fmt.Fprintf(&builder, "# Managed by OneinStack Panel. Do not edit.\n# revision=%s\n\n", policy.Revision)
	if policy.DetectorJail != "" {
		fmt.Fprintf(&builder, "[%s]\nenabled = %s\nfilter = %s\n", policy.DetectorJail, enabled, filterName)
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
		case "mysql-auth", "redis-auth", "vsftpd-auth":
			path, err := detectionLogPath(policy.Template, policy.Enabled)
			if err != nil {
				return nil, err
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

func templateFilterName(template string) string {
	switch template {
	case "mysql-auth":
		return "mysqld-auth"
	case "redis-auth":
		return "redis"
	case "vsftpd-auth":
		return "vsftpd"
	default:
		return template
	}
}

func detectionLogPath(template string, required bool) (string, error) {
	filter := templateFilterName(template)
	filterFound := false
	for _, root := range []string{"/etc/fail2ban/filter.d", "/usr/share/fail2ban/filter.d"} {
		if info, err := os.Stat(filepath.Join(root, filter+".conf")); err == nil && info.Mode().IsRegular() {
			filterFound = true
			break
		}
	}
	if required && !filterFound {
		return "", validation(fmt.Sprintf("%s 防护不可用：未找到 Fail2ban 过滤器 %s，请先安装对应的 Fail2ban 过滤器包", templateDisplayName(template), filter))
	}

	paths := detectionLogCandidates(template)
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path, nil
		}
	}
	if required {
		return "", validation(fmt.Sprintf("%s 防护不可用：未找到可读取的日志文件，已检查 %s", templateDisplayName(template), strings.Join(paths, "、")))
	}
	return paths[0], nil
}

func detectionLogCandidates(template string) []string {
	switch template {
	case "mysql-auth":
		return []string{"/data/mysql/mysql-error.log", "/var/lib/mysql/mysql-error.log", "/var/log/mysql/error.log", "/var/log/mysqld.log"}
	case "redis-auth":
		return []string{"/usr/local/redis/var/redis.log", "/var/log/redis/redis-server.log", "/var/log/redis.log"}
	case "vsftpd-auth":
		return []string{"/var/log/vsftpd.log", "/var/log/secure", "/var/log/auth.log"}
	default:
		return []string{}
	}
}

func templateDisplayName(template string) string {
	if item, ok := templateByKey(template); ok {
		return item.Name
	}
	return template
}

func (s *Service) validateAndReload(ctx context.Context) error {
	if _, err := run(ctx, "-t"); err != nil {
		return fmt.Errorf("%w: %v", ErrConfigValidation, err)
	}
	if _, err := run(ctx, "reload"); err != nil {
		return fmt.Errorf("%w: %v", ErrReload, err)
	}
	return nil
}

func (s *Service) Status(ctx context.Context) (*Status, error) {
	result := &Status{Jails: []string{}}
	if s == nil || s.db == nil {
		return nil, ErrUnavailable
	}
	if state, err := ensureState(s.db); err == nil {
		result.Migration = state
	}
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

type banInfo struct {
	IP        string
	BannedAt  time.Time
	ExpiresAt time.Time
}

func (s *Service) ListBans(ctx context.Context) ([]Ban, error) {
	var policies []models.Fail2banPolicy
	if err := s.db.Where("enabled = ?", true).Order("created_at ASC").Find(&policies).Error; err != nil {
		return nil, err
	}
	var records []models.Fail2banBan
	if err := s.db.Find(&records).Error; err != nil {
		return nil, err
	}
	recordByKey := make(map[string]models.Fail2banBan, len(records))
	for _, record := range records {
		recordByKey[banRecordKey(record.PolicyID, record.IP)] = record
	}
	result := make([]Ban, 0)
	for _, policy := range policies {
		infos, err := s.listPolicyBans(ctx, policy)
		if err != nil {
			result = appendPersistedBans(result, policy, recordByKey)
			continue
		}
		for _, info := range infos {
			banTime := policy.BanTimeSeconds
			expiresAt := info.ExpiresAt
			record, hasRecord := recordByKey[banRecordKey(policy.ID, info.IP)]
			if hasRecord && strings.TrimSpace(record.TaskID) != "" {
				// Panel-created bans may use a one-off duration. Fail2ban's
				// runtime bantime is restored to the policy default after the
				// manual ban, so the persisted Panel deadline is authoritative.
				banTime = record.BanTimeSeconds
				expiresAt = record.ExpiresAt
			} else if !info.BannedAt.IsZero() && !expiresAt.IsZero() {
				if seconds := int(expiresAt.Sub(info.BannedAt).Seconds()); seconds > 0 {
					banTime = seconds
				}
			} else if hasRecord {
				banTime = record.BanTimeSeconds
				expiresAt = record.ExpiresAt
			}
			createdAt := banCreatedAt(info.BannedAt, record, hasRecord)
			var expiry *time.Time
			if !expiresAt.IsZero() {
				expiresAt = expiresAt.UTC()
				expiry = &expiresAt
			}
			result = append(result, Ban{
				PolicyID: policy.ID, Policy: policy.Name, Jail: policy.JailName,
				IP: info.IP, Managed: true, BanTime: banTime, CreatedAt: createdAt, ExpiresAt: expiry,
			})
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		return banCreatedAtBefore(result[i], result[j])
	})
	return result, nil
}

// appendPersistedBans is only used when the runtime jail cannot be queried.
// A successful Panel task has already verified the ban and persisted its
// deadline; using that short-lived record keeps the list useful during a
// transient Fail2ban query failure without replacing runtime state as truth.
func appendPersistedBans(result []Ban, policy models.Fail2banPolicy, records map[string]models.Fail2banBan) []Ban {
	now := time.Now().UTC()
	for _, record := range records {
		if record.PolicyID != policy.ID || strings.TrimSpace(record.TaskID) == "" || !record.ExpiresAt.After(now) {
			continue
		}
		expiresAt := record.ExpiresAt.UTC()
		createdAt := banCreatedAt(record.BannedAt, record, true)
		result = append(result, Ban{
			PolicyID: policy.ID, Policy: policy.Name, Jail: record.Jail, IP: record.IP,
			Managed: true, BanTime: record.BanTimeSeconds, CreatedAt: createdAt, ExpiresAt: &expiresAt,
		})
	}
	return result
}

func banCreatedAt(runtimeBannedAt time.Time, record models.Fail2banBan, hasRecord bool) *time.Time {
	createdAt := runtimeBannedAt
	if hasRecord {
		if !record.BannedAt.IsZero() {
			createdAt = record.BannedAt
		} else if createdAt.IsZero() {
			createdAt = record.CreatedAt
		}
	}
	if createdAt.IsZero() {
		return nil
	}
	createdAt = createdAt.UTC()
	return &createdAt
}

func banCreatedAtBefore(left, right Ban) bool {
	if left.CreatedAt == nil {
		return false
	}
	if right.CreatedAt == nil {
		return true
	}
	if !left.CreatedAt.Equal(*right.CreatedAt) {
		return left.CreatedAt.Before(*right.CreatedAt)
	}
	if left.PolicyID != right.PolicyID {
		return left.PolicyID < right.PolicyID
	}
	return left.IP < right.IP
}

func (s *Service) listPolicyBans(ctx context.Context, policy models.Fail2banPolicy) ([]banInfo, error) {
	output, err := run(ctx, "get", policy.JailName, "banip", "--with-time")
	if err == nil {
		infos := parseBannedIPDetails(output)
		if len(infos) > 0 || strings.TrimSpace(output) == "" {
			return infos, nil
		}
	}
	output, err = run(ctx, "status", policy.JailName)
	if err != nil {
		return nil, err
	}
	addresses := parseBannedIPs(output)
	infos := make([]banInfo, 0, len(addresses))
	for _, address := range addresses {
		infos = append(infos, banInfo{IP: address})
	}
	return infos, nil
}

// SyncBanRecords imports active bans and their actual Fail2ban deadlines into
// Panel state. This also adopts bans created before the expiry table existed.
func (s *Service) SyncBanRecords(ctx context.Context) error {
	var policies []models.Fail2banPolicy
	if err := s.db.Order("created_at ASC").Find(&policies).Error; err != nil {
		return err
	}
	var records []models.Fail2banBan
	if err := s.db.Find(&records).Error; err != nil {
		return err
	}
	recordByKey := make(map[string]models.Fail2banBan, len(records))
	for _, record := range records {
		recordByKey[banRecordKey(record.PolicyID, record.IP)] = record
	}
	for _, policy := range policies {
		infos, err := s.listPolicyBans(ctx, policy)
		if err != nil {
			continue
		}
		for _, info := range infos {
			if info.ExpiresAt.IsZero() {
				continue
			}
			if record, ok := recordByKey[banRecordKey(policy.ID, info.IP)]; ok && strings.TrimSpace(record.TaskID) != "" {
				// Keep the Panel-requested one-off deadline. The collector will
				// submit the idempotent unban task when that deadline is reached.
				continue
			}
			banTime := policy.BanTimeSeconds
			bannedAt := info.BannedAt
			if !bannedAt.IsZero() {
				if seconds := int(info.ExpiresAt.Sub(bannedAt).Seconds()); seconds > 0 {
					banTime = seconds
				}
			} else {
				bannedAt = info.ExpiresAt.Add(-time.Duration(banTime) * time.Second)
			}
			record := models.Fail2banBan{
				PolicyID: policy.ID, Jail: policy.JailName, IP: info.IP,
				BanTimeSeconds: banTime, BannedAt: bannedAt.UTC(), ExpiresAt: info.ExpiresAt.UTC(),
			}
			if err := s.upsertBanRecord(record); err != nil {
				return err
			}
			if err := s.ensureActiveBanAudit(policy, record); err != nil {
				log.Printf("persist fail2ban active ban audit for %s: %v", record.IP, err)
			}
		}
	}
	return nil
}

// ensureActiveBanAudit records a ban discovered in Fail2ban runtime state.
// Panel-created bans have a task ID and are audited by the task worker; this
// path covers bans created outside Panel or imported from an older runtime.
func (s *Service) ensureActiveBanAudit(policy models.Fail2banPolicy, ban models.Fail2banBan) error {
	manager := auditservice.Default()
	if manager == nil {
		return nil
	}
	requestID := digest("fail2ban-active-ban|" + policy.ID + "|" + ban.IP + "|" +
		ban.BannedAt.UTC().Format(time.RFC3339Nano) + "|" + ban.ExpiresAt.UTC().Format(time.RFC3339Nano))
	var existing models.AuditEvent
	err := s.db.Where("request_id = ? AND action = ?", requestID, "fail2ban.ban").First(&existing).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	createdAt := ban.BannedAt
	if createdAt.IsZero() {
		createdAt = ban.CreatedAt
	}
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	message := fmt.Sprintf(
		"Fail2ban 检测到实时封禁，IP=%s，策略=%s，Jail=%s，封禁时长=%d秒，到期时间=%s，触发方式=external",
		ban.IP, policy.Name, ban.Jail, ban.BanTimeSeconds, ban.ExpiresAt.UTC().Format(time.RFC3339),
	)
	_, err = manager.Append(auditservice.EventInput{
		RequestID: requestID,
		EventType: "security",
		Action:    "fail2ban.ban",
		Method:    "WORKER",
		Route:     "/v1/security/fail2ban/bans",
		Path:      "/v1/security/fail2ban/bans",
		Status:    200,
		Outcome:   "success",
		Sensitive: true,
		AuthMode:  "external",
		RemoteIP:  ban.IP,
		Message:   message,
		CreatedAt: createdAt.UTC(),
	})
	return err
}

func (s *Service) upsertBanRecord(record models.Fail2banBan) error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	if strings.TrimSpace(record.ID) == "" {
		record.ID = uuid.NewString()
	}
	values := map[string]any{
		"jail": record.Jail, "ban_time_seconds": record.BanTimeSeconds,
		"banned_at": record.BannedAt, "expires_at": record.ExpiresAt,
	}
	if record.TaskID != "" {
		values["task_id"] = record.TaskID
	}
	return s.db.Where("policy_id = ? AND ip = ?", record.PolicyID, record.IP).
		Assign(values).FirstOrCreate(&record).Error
}

func banRecordKey(policyID, ip string) string { return policyID + "\x00" + ip }

func (s *Service) ResolveBanRequest(request BanRequest) (BanRequest, *models.Fail2banPolicy, *models.SecurityIncident, error) {
	return s.resolveBanRequest(request, false)
}

func (s *Service) ResolveUnbanRequest(request BanRequest) (BanRequest, *models.Fail2banPolicy, *models.SecurityIncident, error) {
	return s.resolveBanRequest(request, true)
}

func (s *Service) resolveBanRequest(request BanRequest, allowDisabled bool) (BanRequest, *models.Fail2banPolicy, *models.SecurityIncident, error) {
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
	if !allowDisabled && !policy.Enabled {
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
	if request.BanMinutes < 0 || request.BanMinutes > 525600 {
		return request, nil, nil, validation("封禁时间必须在 300-31536000 秒之间")
	}
	if request.BanTimeSeconds != 0 && request.BanMinutes != 0 && request.BanTimeSeconds != request.BanMinutes*60 {
		return request, nil, nil, validation("banTimeSeconds 与 banMinutes 不能同时指定不同值")
	}
	if request.BanTimeSeconds == 0 && request.BanMinutes > 0 {
		request.BanTimeSeconds = request.BanMinutes * 60
	}
	request.BanMinutes = 0
	if request.BanTimeSeconds == 0 {
		request.BanTimeSeconds = policy.BanTimeSeconds
	}
	if request.BanTimeSeconds < 300 || request.BanTimeSeconds > 31536000 {
		return request, nil, nil, validation("封禁时间必须在 300-31536000 秒之间")
	}
	return request, &policy, incident, nil
}

func (s *Service) Ban(ctx context.Context, request BanRequest, taskID string) error {
	request, policy, incident, err := s.ResolveBanRequest(request)
	if err != nil {
		return err
	}
	s.banMu.Lock()
	defer s.banMu.Unlock()
	var banErr error
	var restoreErr error
	restoreBanTime := false
	if request.BanTimeSeconds != policy.BanTimeSeconds {
		if _, err := run(ctx, "set", policy.JailName, "bantime", strconv.Itoa(request.BanTimeSeconds)); err != nil {
			return err
		}
		restoreBanTime = true
	}
	_, banErr = run(ctx, "set", policy.JailName, "banip", request.IP)
	if restoreBanTime {
		restoreCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_, restoreErr = run(restoreCtx, "set", policy.JailName, "bantime", strconv.Itoa(policy.BanTimeSeconds))
		cancel()
	}
	if banErr != nil {
		output, statusErr := run(ctx, "status", policy.JailName)
		if statusErr == nil && containsAddress(parseBannedIPs(output), request.IP) {
			banErr = nil
		}
	}
	if banErr != nil || restoreErr != nil {
		if restoreErr != nil {
			return errors.Join(banErr, fmt.Errorf("恢复 Fail2ban 默认封禁时长失败: %w", restoreErr))
		}
		return banErr
	}
	now := time.Now().UTC()
	if err := s.upsertBanRecord(models.Fail2banBan{
		PolicyID: policy.ID, Jail: policy.JailName, IP: request.IP,
		BanTimeSeconds: request.BanTimeSeconds, BannedAt: now,
		ExpiresAt: now.Add(time.Duration(request.BanTimeSeconds) * time.Second), TaskID: taskID,
	}); err != nil {
		return err
	}
	if incident != nil {
		_ = s.db.Model(&models.SecurityIncident{}).Where("id = ?", incident.ID).Updates(map[string]any{
			"status": "blocked", "task_id": taskID, "resolved_at": &now, "updated_at": now,
		}).Error
	}
	return nil
}

func (s *Service) Unban(ctx context.Context, request BanRequest) error {
	request, policy, _, err := s.ResolveUnbanRequest(request)
	if err != nil {
		return err
	}
	if _, err := run(ctx, "set", policy.JailName, "unbanip", request.IP); err != nil {
		output, statusErr := run(ctx, "status", policy.JailName)
		if statusErr != nil || containsAddress(parseBannedIPs(output), request.IP) {
			return err
		}
	}
	return s.db.Where("policy_id = ? AND ip = ?", policy.ID, request.IP).Delete(&models.Fail2banBan{}).Error
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
		message := fmt.Sprintf("fail2ban-client %s failed", strings.Join(args, " "))
		if detail := sanitizeCommandOutput(text); detail != "" {
			message += ": " + detail
		}
		return text, errors.New(message)
	}
	return text, nil
}

func sanitizeCommandOutput(value string) string {
	value = strings.Map(func(char rune) rune {
		if unicode.IsControl(char) {
			return ' '
		}
		return char
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	const maxRunes = 320
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "..."
	}
	return value
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

func parseBannedIPDetails(output string) []banInfo {
	result := make([]banInfo, 0)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || net.ParseIP(fields[0]) == nil {
			continue
		}
		info := banInfo{IP: net.ParseIP(fields[0]).String()}
		if len(fields) >= 3 {
			info.BannedAt = parseBanTime(fields[1], fields[2])
		}
		for index, field := range fields {
			if field != "=" || index+2 >= len(fields) {
				continue
			}
			info.ExpiresAt = parseBanTime(fields[index+1], fields[index+2])
			break
		}
		result = append(result, info)
	}
	return result
}

func parseBanTime(date, clock string) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", date+" "+clock, time.Local)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func containsAddress(values []string, address string) bool {
	for _, value := range values {
		if value == address {
			return true
		}
	}
	return false
}

func eventSpoolPath() string { return eventFilePath }

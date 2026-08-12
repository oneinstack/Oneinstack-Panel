package safe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services"
	"oneinstack/router/input"
	"oneinstack/router/output"
)

type Service struct {
	db             *gorm.DB
	runner         CommandRunner
	panelPort      int
	ufwBeforeRules string
}

var operationMu sync.Mutex

func NewService(db *gorm.DB, runner CommandRunner, panelPort int) *Service {
	if runner == nil {
		runner = OSCommandRunner{}
	}
	if panelPort < 1 || panelPort > 65535 {
		panelPort = 8089
	}
	return &Service{
		db: db, runner: runner, panelPort: panelPort,
		ufwBeforeRules: "/etc/ufw/before.rules",
	}
}

func NewDefaultService() *Service {
	return NewService(app.DB(), OSCommandRunner{}, parsePanelPort(app.ONE_CONFIG.System.Port))
}

func (s *Service) Status(ctx context.Context) (*output.IptablesStatus, error) {
	state := s.detectBackend(ctx)
	status := &output.IptablesStatus{
		Install: state.Installed, Enabled: state.Enabled, Backend: state.Name,
		RuntimeBackend: state.Name,
		Persistent:     state.Persistent, CanToggle: state.CanToggle,
		RepairRequired: state.RepairRequired,
		Warning:        state.Warning, PanelPort: s.panelPort,
	}
	if s.db != nil {
		if err := s.db.Model(&models.IptablesRule{}).Count(&status.ManagedRuleCount).Error; err != nil {
			return nil, err
		}
		if err := s.loadRuleCounts(&status.Counts); err != nil {
			return nil, err
		}
		var managedProtectedRule models.IptablesRule
		managedResult := s.db.Where(
			"protected = ? AND protocol = ? AND strategy = ? AND ports = ?",
			true, "tcp", "allow", fmt.Sprint(s.panelPort),
		).Order("id DESC").First(&managedProtectedRule)
		if managedResult.Error == nil {
			status.ManagedBackend = managedProtectedRule.Backend
			status.ManagedPanelRule = true
			if !status.Install && status.ManagedBackend != "" {
				status.Install = true
				status.Backend = status.ManagedBackend
				status.Persistent = backendPersistentDefault(status.ManagedBackend)
				status.Warning = "检测到面板受管防火墙规则，但当前未检测到对应防火墙运行时，请先修复主机防火墙环境"
			} else if status.ManagedBackend != "" &&
				state.Name != BackendNone &&
				status.ManagedBackend != state.Name {
				status.Warning = appendWarning(
					status.Warning,
					"受管规则后端与当前运行时后端不一致，请核对防火墙切换或持久化状态",
				)
			}
		} else if !errors.Is(managedResult.Error, gorm.ErrRecordNotFound) {
			return nil, managedResult.Error
		}
		var protectedRule models.IptablesRule
		result := s.db.Where(
			"protected = ? AND protocol = ? AND strategy = ? AND ports = ? AND backend = ?",
			true, "tcp", "allow", fmt.Sprint(s.panelPort), state.Name,
		).First(&protectedRule)
		if result.Error == nil {
			status.PanelPortProtected = s.ruleExists(ctx, &protectedRule, state)
		} else if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, result.Error
		}
		if !status.PanelPortProtected && status.ManagedPanelRule && state.Name == BackendNone {
			status.Warning = appendWarning(status.Warning, "面板端口保护规则记录仍在，但当前无法确认系统规则已生效")
		}
	}
	blocked, err := s.pingBlocked(ctx, state)
	if err != nil && status.Install {
		status.Warning = appendWarning(status.Warning, "无法确认 ICMP 状态："+err.Error())
	} else {
		status.PingBlocked = blocked
	}
	return status, nil
}

func (s *Service) List(param *input.IptablesRuleParam) (*services.PaginatedResult[models.IptablesRule], error) {
	tx := s.db.Model(&models.IptablesRule{})
	if query := strings.TrimSpace(param.Q); query != "" {
		like := "%" + query + "%"
		tx = tx.Where("remark LIKE ? OR ips LIKE ? OR location LIKE ?", like, like, like)
	}
	if direction := strings.TrimSpace(param.Direction); direction != "" {
		tx = tx.Where("direction = ?", direction)
	}
	if ruleType := strings.TrimSpace(param.RuleType); ruleType != "" {
		if ruleType == "port" {
			tx = tx.Where("rule_type = ? OR rule_type IS NULL OR rule_type = ''", ruleType)
		} else {
			tx = tx.Where("rule_type = ?", ruleType)
		}
	}
	if param.State != nil {
		tx = tx.Where("state = ?", *param.State)
	}
	tx = tx.Order("protected DESC, id DESC")
	result, err := services.Paginate[models.IptablesRule](tx, &models.IptablesRule{}, &input.Page{
		Page: param.Page.Page, PageSize: param.Page.PageSize,
	})
	if err != nil {
		return nil, err
	}
	for index := range result.Data {
		normalizeStoredRule(&result.Data[index])
	}
	return result, nil
}

func (s *Service) Add(ctx context.Context, rule *models.IptablesRule) error {
	operationMu.Lock()
	defer operationMu.Unlock()
	return s.addLocked(ctx, rule)
}

func (s *Service) addLocked(ctx context.Context, rule *models.IptablesRule) error {
	normalized, err := normalizeRule(rule, s.panelPort)
	if err != nil {
		return err
	}
	state := s.detectBackend(ctx)
	if !state.Installed {
		return fmt.Errorf("%w: 未检测到受支持的防火墙", ErrUnsupported)
	}
	applyNormalized(rule, normalized)
	rule.ID = 0
	rule.Backend = state.Name
	rule.Token = uuid.NewString()
	rule.Protected = false
	if rule.Location == "" {
		rule.Location = describeIPLocation(rule.IPs)
	}
	var operations []commandOperation
	if rule.State == 1 {
		operations, err = s.ruleOperations(rule)
		if err != nil {
			return err
		}
		if err := s.runOperations(ctx, rule.Backend, operations); err != nil {
			return fmt.Errorf("应用防火墙规则失败: %w", err)
		}
	}
	if err := s.db.Create(rule).Error; err != nil {
		if rule.State == 1 {
			s.rollbackOperations(ctx, operations)
			_ = s.persist(ctx, rule.Backend)
		}
		return fmt.Errorf("保存规则失败，系统规则已回滚: %w", err)
	}
	return nil
}

// EnsureWebsitePort opens an inbound TCP port only when the detected firewall
// is already enabled. Existing active rules that allow the port from all IPv4
// addresses are reused so creating several virtual hosts on port 80/443 does
// not create duplicate host rules.
func (s *Service) EnsureWebsitePort(
	ctx context.Context,
	port int,
	websiteName string,
) (int64, bool, error) {
	if port < 1 || port > 65535 {
		return 0, false, validationError("网站端口必须在 1-65535 之间")
	}
	websiteName = strings.TrimSpace(websiteName)
	if websiteName == "" {
		return 0, false, validationError("网站名称不能为空")
	}

	operationMu.Lock()
	defer operationMu.Unlock()

	state := s.detectBackend(ctx)
	if !state.Installed || !state.Enabled {
		return 0, false, nil
	}

	var candidates []models.IptablesRule
	if err := s.db.Where(
		"state = ? AND direction = ? AND strategy = ? AND protocol IN ?",
		1, "in", "allow", []string{"tcp", "all"},
	).Order("protected DESC, id DESC").Find(&candidates).Error; err != nil {
		return 0, false, err
	}
	for index := range candidates {
		candidate := &candidates[index]
		normalized, err := normalizeRule(candidate, s.panelPort)
		if err != nil || !containsAllIPv4(normalized.IPs) ||
			!portSetContains(normalized.Ports, port) {
			continue
		}
		if candidate.Backend != "" && candidate.Backend != state.Name {
			continue
		}
		if s.ruleExists(ctx, candidate, state) {
			return candidate.ID, false, nil
		}
	}

	rule := &models.IptablesRule{
		RuleType: "port", Direction: "in", Protocol: "tcp", Strategy: "allow",
		IPs: "0.0.0.0/0", Ports: strconv.Itoa(port), State: 1, Remark: websiteName,
	}
	if err := s.addLocked(ctx, rule); err != nil {
		return 0, false, err
	}
	return rule.ID, true, nil
}

func (s *Service) Update(ctx context.Context, requested *models.IptablesRule) error {
	operationMu.Lock()
	defer operationMu.Unlock()

	if requested.ID < 1 {
		return validationError("规则 ID 无效")
	}
	var old models.IptablesRule
	if err := s.db.First(&old, requested.ID).Error; err != nil {
		return err
	}
	if old.Protected {
		return fmt.Errorf("%w: 面板端口保护规则不能编辑", ErrProtected)
	}
	normalized, err := normalizeRule(requested, s.panelPort)
	if err != nil {
		return err
	}
	applyNormalized(requested, normalized)
	requested.Backend = old.Backend
	if requested.Backend == "" {
		requested.Backend = s.detectBackend(ctx).Name
	}
	requested.Token = old.Token
	if requested.Token == "" {
		requested.Token = uuid.NewString()
	}
	requested.Protected = false
	if requested.Location == "" {
		requested.Location = describeIPLocation(requested.IPs)
	}

	old.Backend = requested.Backend
	var oldOperations, newOperations []commandOperation
	if old.State == 1 {
		oldOperations, err = s.ruleOperations(&old)
		if err != nil {
			return err
		}
		if err := s.runOperations(ctx, old.Backend, reverseOperations(oldOperations)); err != nil {
			return fmt.Errorf("删除旧系统规则失败，原规则已恢复: %w", err)
		}
	}
	if requested.State == 1 {
		newOperations, err = s.ruleOperations(requested)
		if err != nil {
			if old.State == 1 {
				_ = s.runOperations(ctx, old.Backend, oldOperations)
			}
			return err
		}
		if err := s.runOperations(ctx, requested.Backend, newOperations); err != nil {
			if old.State == 1 {
				_ = s.runOperations(ctx, old.Backend, oldOperations)
			}
			return fmt.Errorf("应用新规则失败，原规则已恢复: %w", err)
		}
	}
	updates := map[string]any{
		"rule_type": requested.RuleType,
		"direction": requested.Direction, "protocol": requested.Protocol,
		"strategy": requested.Strategy, "ips": requested.IPs, "ports": requested.Ports,
		"state": requested.State, "remark": requested.Remark, "location": requested.Location,
		"expires_at": requested.ExpiresAt, "backend": requested.Backend,
		"token": requested.Token,
	}
	if err := s.db.Model(&models.IptablesRule{}).Where("id = ?", old.ID).Updates(updates).Error; err != nil {
		if requested.State == 1 {
			_ = s.runOperations(ctx, requested.Backend, reverseOperations(newOperations))
		}
		if old.State == 1 {
			_ = s.runOperations(ctx, old.Backend, oldOperations)
		}
		return fmt.Errorf("保存规则失败，原规则已恢复: %w", err)
	}
	if err := s.db.First(requested, old.ID).Error; err != nil {
		return fmt.Errorf("读取更新后的规则失败: %w", err)
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	operationMu.Lock()
	defer operationMu.Unlock()
	return s.deleteLocked(ctx, id)
}

func (s *Service) deleteLocked(ctx context.Context, id int64) error {
	if id < 1 {
		return validationError("规则 ID 无效")
	}
	var rule models.IptablesRule
	if err := s.db.First(&rule, id).Error; err != nil {
		return err
	}
	if rule.Protected {
		return fmt.Errorf("%w: 面板端口保护规则不能删除", ErrProtected)
	}
	if rule.Backend == "" {
		rule.Backend = s.detectBackend(ctx).Name
	}
	var operations []commandOperation
	var err error
	if rule.State == 1 {
		operations, err = s.ruleOperations(&rule)
		if err != nil {
			return err
		}
		if err := s.runOperations(ctx, rule.Backend, reverseOperations(operations)); err != nil {
			return fmt.Errorf("删除系统规则失败，原规则已恢复: %w", err)
		}
	}
	if err := s.db.Delete(&models.IptablesRule{}, rule.ID).Error; err != nil {
		if rule.State == 1 {
			_ = s.runOperations(ctx, rule.Backend, operations)
		}
		return fmt.Errorf("删除规则记录失败，系统规则已恢复: %w", err)
	}
	return nil
}

func (s *Service) SetRuleState(ctx context.Context, id int64, enabled bool) error {
	operationMu.Lock()
	defer operationMu.Unlock()
	return s.setRuleStateLocked(ctx, id, enabled)
}

func (s *Service) setRuleStateLocked(ctx context.Context, id int64, enabled bool) error {
	if id < 1 {
		return validationError("规则 ID 无效")
	}
	var rule models.IptablesRule
	if err := s.db.First(&rule, id).Error; err != nil {
		return err
	}
	if rule.Protected {
		return fmt.Errorf("%w: 面板端口保护规则不能停用", ErrProtected)
	}
	target := 0
	if enabled {
		target = 1
	}
	if rule.State == target {
		return nil
	}
	if enabled && rule.ExpiresAt != nil && !rule.ExpiresAt.After(time.Now()) {
		return validationError("过期规则不能重新启用，请先修改过期时间")
	}
	if rule.Backend == "" {
		rule.Backend = s.detectBackend(ctx).Name
	}
	operations, err := s.ruleOperations(&rule)
	if err != nil {
		return err
	}
	run := operations
	if !enabled {
		run = reverseOperations(operations)
	}
	if err := s.runOperations(ctx, rule.Backend, run); err != nil {
		return fmt.Errorf("更新系统规则状态失败: %w", err)
	}
	if err := s.db.Model(&models.IptablesRule{}).Where("id = ?", id).Update("state", target).Error; err != nil {
		s.rollbackOperations(ctx, run)
		_ = s.persist(ctx, rule.Backend)
		return fmt.Errorf("保存规则状态失败，系统规则已回滚: %w", err)
	}
	return nil
}

func (s *Service) Batch(ctx context.Context, ids []int64, action string) (int, error) {
	operationMu.Lock()
	defer operationMu.Unlock()
	if len(ids) == 0 {
		return 0, validationError("请选择至少一条规则")
	}
	if len(ids) > 500 {
		return 0, validationError("单次批量操作不能超过 500 条规则")
	}
	action = strings.ToLower(strings.TrimSpace(action))
	completed := 0
	for _, id := range uniqueIDs(ids) {
		var err error
		switch action {
		case "enable":
			err = s.setRuleStateLocked(ctx, id, true)
		case "disable":
			err = s.setRuleStateLocked(ctx, id, false)
		case "delete":
			err = s.deleteLocked(ctx, id)
		default:
			return completed, validationError("批量操作仅支持 enable、disable 或 delete")
		}
		if err != nil {
			return completed, fmt.Errorf("已完成 %d 条，第 %d 条失败: %w", completed, id, err)
		}
		completed++
	}
	return completed, nil
}

func (s *Service) CleanupExpired(ctx context.Context) (int, error) {
	operationMu.Lock()
	defer operationMu.Unlock()
	var rules []models.IptablesRule
	if err := s.db.Where("expires_at IS NOT NULL AND expires_at <= ?", time.Now()).
		Order("id ASC").Find(&rules).Error; err != nil {
		return 0, err
	}
	cleaned := 0
	for _, rule := range rules {
		if rule.Protected {
			continue
		}
		if err := s.deleteLocked(ctx, rule.ID); err != nil {
			return cleaned, err
		}
		cleaned++
	}
	return cleaned, nil
}

func (s *Service) ExportRules(ruleType string) ([]models.IptablesRule, error) {
	tx := s.db.Where("protected = ?", false)
	if ruleType = strings.TrimSpace(ruleType); ruleType != "" {
		if ruleType == "port" {
			tx = tx.Where("rule_type = ? OR rule_type IS NULL OR rule_type = ''", ruleType)
		} else {
			tx = tx.Where("rule_type = ?", ruleType)
		}
	}
	var rules []models.IptablesRule
	if err := tx.Order("id ASC").Find(&rules).Error; err != nil {
		return nil, err
	}
	for index := range rules {
		normalizeStoredRule(&rules[index])
		rules[index].ID = 0
		rules[index].Backend = ""
		rules[index].Token = ""
		rules[index].Protected = false
	}
	return rules, nil
}

func (s *Service) ImportRules(ctx context.Context, rules []models.IptablesRule) (int, error) {
	if len(rules) == 0 {
		return 0, validationError("导入文件中没有规则")
	}
	if len(rules) > 500 {
		return 0, validationError("单次最多导入 500 条规则")
	}
	created := make([]int64, 0, len(rules))
	for index := range rules {
		rules[index].ID = 0
		rules[index].Protected = false
		if err := s.Add(ctx, &rules[index]); err != nil {
			for rollback := len(created) - 1; rollback >= 0; rollback-- {
				_ = s.Delete(ctx, created[rollback])
			}
			return 0, fmt.Errorf("第 %d 条规则导入失败，已回滚本次导入: %w", index+1, err)
		}
		created = append(created, rules[index].ID)
	}
	return len(created), nil
}

// ReplaceRules restores the non-protected managed rules as one compensated
// operation. The panel protection rule is never removed by this method.
func (s *Service) ReplaceRules(ctx context.Context, rules []models.IptablesRule) error {
	operationMu.Lock()
	defer operationMu.Unlock()
	var current []models.IptablesRule
	if err := s.db.Where("protected = ?", false).Order("id ASC").Find(&current).Error; err != nil {
		return err
	}
	for _, rule := range current {
		if err := s.deleteLocked(ctx, rule.ID); err != nil {
			return err
		}
	}
	created := make([]int64, 0, len(rules))
	for i := range rules {
		rules[i].ID = 0
		rules[i].Protected = false
		if err := s.addLocked(ctx, &rules[i]); err != nil {
			for j := len(created) - 1; j >= 0; j-- {
				_ = s.deleteLocked(ctx, created[j])
			}
			for j := range current {
				_ = s.addLocked(ctx, &current[j])
			}
			return fmt.Errorf("restore firewall rules at %d: %w", i+1, err)
		}
		created = append(created, rules[i].ID)
	}
	return nil
}

func (s *Service) SetEnabled(ctx context.Context, enabled bool, confirmation string) error {
	operationMu.Lock()
	defer operationMu.Unlock()

	state := s.detectBackend(ctx)
	if !state.Installed {
		return fmt.Errorf("%w: 未检测到受支持的防火墙", ErrUnsupported)
	}
	if state.Enabled == enabled {
		return nil
	}
	if !enabled && strings.TrimSpace(confirmation) != DisableConfirmation {
		return validationError("关闭防火墙需要输入确认文本 " + DisableConfirmation)
	}
	if enabled {
		if state.RepairRequired {
			return fmt.Errorf("%w: firewalld 配置校验失败，请先运行修复任务", ErrUnsupported)
		}
		if !state.CanToggle {
			return fmt.Errorf("%w: 当前环境没有可用的 systemd，无法启用主机 firewalld", ErrUnsupported)
		}
		created, operations, err := s.ensurePanelRule(ctx, state)
		if err != nil {
			return err
		}
		if err := s.toggleBackend(ctx, state.Name, true); err != nil {
			if created != nil {
				s.rollbackOperations(ctx, operations)
				_ = s.db.Delete(&models.IptablesRule{}, created.ID).Error
			}
			return err
		}
		return nil
	}
	return s.toggleBackend(ctx, state.Name, false)
}

func (s *Service) ensurePanelRule(ctx context.Context, state backendState) (*models.IptablesRule, []commandOperation, error) {
	return s.ensureProtectedPort(ctx, state, s.panelPort)
}

func (s *Service) ensureProtectedPort(ctx context.Context, state backendState, port int) (*models.IptablesRule, []commandOperation, error) {
	var existing models.IptablesRule
	result := s.db.Where(
		"protected = ? AND protocol = ? AND strategy = ? AND ports = ? AND backend = ?",
		true, "tcp", "allow", fmt.Sprint(port), state.Name,
	).First(&existing)
	if result.Error == nil {
		if s.ruleExists(ctx, &existing, state) {
			return nil, nil, nil
		}
		// A database-only protection marker must never suppress recreation of
		// the actual system rule. Remove the stale marker and create a fresh,
		// independently identifiable rule below.
		if err := s.db.Delete(&models.IptablesRule{}, existing.ID).Error; err != nil {
			return nil, nil, err
		}
	} else if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, nil, result.Error
	}
	rule := &models.IptablesRule{
		Direction: "in", Protocol: "tcp", Strategy: "allow",
		IPs: "0.0.0.0/0", Ports: fmt.Sprint(port), State: 1,
		Remark: panelRuleRemark, Backend: state.Name, Token: uuid.NewString(), Protected: true,
	}
	var operations []commandOperation
	var err error
	if state.Name == BackendFirewalld && !state.Enabled {
		if _, pathErr := s.runner.LookPath("firewall-offline-cmd"); pathErr != nil {
			return nil, nil, fmt.Errorf("%w: firewalld 未运行且缺少 firewall-offline-cmd，无法安全预置面板端口", ErrUnsupported)
		}
		operations = firewalldRuleOperations(rule, "firewall-offline-cmd", false)
	} else {
		operations, err = s.ruleOperations(rule)
		if err != nil {
			return nil, nil, err
		}
	}
	if err := s.runOperations(ctx, offlinePersistBackend(state), operations); err != nil {
		return nil, nil, fmt.Errorf("预置面板端口保护规则失败: %w", err)
	}
	if err := s.db.Create(rule).Error; err != nil {
		s.rollbackOperations(ctx, operations)
		_ = s.persist(ctx, offlinePersistBackend(state))
		return nil, nil, err
	}
	return rule, operations, nil
}

func (s *Service) ruleExists(ctx context.Context, rule *models.IptablesRule, state backendState) bool {
	switch rule.Backend {
	case BackendUFW:
		output, err := s.runner.Run(ctx, "ufw", "show", "added")
		return err == nil && rule.Token != "" && strings.Contains(string(output), "oneinstack:"+rule.Token)
	case BackendFirewalld:
		command := "firewall-cmd"
		permanent := true
		if !state.Enabled {
			if _, err := s.runner.LookPath("firewall-offline-cmd"); err != nil {
				return false
			}
			command = "firewall-offline-cmd"
			permanent = false
		}
		operations := firewalldRuleOperations(rule, command, permanent)
		if len(operations) == 0 {
			return false
		}
		args := append([]string{}, operations[0].args...)
		for index, argument := range args {
			switch {
			case argument == "--add-rule":
				args[index] = "--query-rule"
			case strings.HasPrefix(argument, "--add-rich-rule="):
				args[index] = "--query-rich-rule=" + strings.TrimPrefix(argument, "--add-rich-rule=")
			}
		}
		_, err := s.runner.Run(ctx, command, args...)
		return err == nil
	case BackendIPTables:
		operations := iptablesRuleOperations(rule)
		if len(operations) == 0 {
			return false
		}
		args := append([]string{}, operations[0].args...)
		args[0] = "-C"
		_, err := s.runner.Run(ctx, "iptables", args...)
		return err == nil
	default:
		return false
	}
}

// PreparePanelPort writes a protected allow rule before a panel port change.
// It returns the created rule ID so callers can roll it back if their own
// configuration transaction fails.
func (s *Service) PreparePanelPort(ctx context.Context, port int) (int64, bool, error) {
	if port < 1 || port > 65535 {
		return 0, false, validationError("面板端口必须在 1-65535 之间")
	}
	operationMu.Lock()
	defer operationMu.Unlock()

	state := s.detectBackend(ctx)
	if !state.Installed {
		return 0, false, nil
	}
	rule, _, err := s.ensureProtectedPort(ctx, state, port)
	if err != nil {
		return 0, false, err
	}
	if rule == nil {
		return 0, false, nil
	}
	return rule.ID, true, nil
}

func (s *Service) RollbackPreparedPanelPort(ctx context.Context, id int64) error {
	if id < 1 {
		return nil
	}
	operationMu.Lock()
	defer operationMu.Unlock()

	var rule models.IptablesRule
	if err := s.db.First(&rule, id).Error; err != nil {
		return err
	}
	if !rule.Protected {
		return fmt.Errorf("%w: 只能回滚系统创建的端口保护规则", ErrProtected)
	}
	operations, err := s.ruleOperations(&rule)
	if err != nil {
		return err
	}
	if err := s.runOperations(ctx, rule.Backend, reverseOperations(operations)); err != nil {
		return err
	}
	if err := s.db.Delete(&models.IptablesRule{}, rule.ID).Error; err != nil {
		_ = s.runOperations(ctx, rule.Backend, operations)
		return err
	}
	return nil
}

func offlinePersistBackend(state backendState) string {
	if state.Name == BackendFirewalld && !state.Enabled {
		return BackendNone
	}
	return state.Name
}

func (s *Service) toggleBackend(ctx context.Context, backend string, enabled bool) error {
	switch backend {
	case BackendUFW:
		action := "disable"
		if enabled {
			action = "enable"
		}
		_, err := s.runner.Run(ctx, "ufw", "--force", action)
		return err
	case BackendFirewalld:
		action := "stop"
		if enabled {
			action = "start"
		}
		_, err := s.runner.Run(ctx, "systemctl", action, "firewalld")
		return err
	default:
		return fmt.Errorf("%w: iptables 后端不支持从面板整体启停", ErrUnsupported)
	}
}

func (s *Service) SetPingBlocked(ctx context.Context, blocked bool) error {
	operationMu.Lock()
	defer operationMu.Unlock()

	state := s.detectBackend(ctx)
	if !state.Installed {
		return fmt.Errorf("%w: 未检测到受支持的防火墙", ErrUnsupported)
	}
	current, statusErr := s.pingBlocked(ctx, state)
	if statusErr == nil && current == blocked {
		return nil
	}
	if statusErr != nil && state.Name == BackendUFW {
		return fmt.Errorf("读取 UFW ICMP 状态失败: %w", statusErr)
	}
	switch state.Name {
	case BackendUFW:
		return s.setUFWPingBlocked(ctx, blocked)
	case BackendFirewalld:
		action := "--remove-icmp-block=echo-request"
		if blocked {
			action = "--add-icmp-block=echo-request"
		}
		operations := []commandOperation{{
			name: "firewall-cmd", args: []string{"--permanent", action},
			undoName: "firewall-cmd", undoArgs: []string{"--permanent", oppositeICMPAction(blocked)},
		}}
		return s.runOperations(ctx, BackendFirewalld, operations)
	case BackendIPTables:
		if !state.Persistent {
			return fmt.Errorf("%w: iptables 缺少 netfilter-persistent", ErrUnsupported)
		}
		add := []string{"-A", "INPUT", "-p", "icmp", "--icmp-type", "echo-request", "-m", "comment", "--comment", "oneinstack:ping", "-j", "DROP"}
		del := append([]string{}, add...)
		del[0] = "-D"
		operation := commandOperation{name: "iptables", args: add, undoName: "iptables", undoArgs: del}
		if !blocked {
			operation = reverseOperations([]commandOperation{operation})[0]
		}
		return s.runOperations(ctx, BackendIPTables, []commandOperation{operation})
	default:
		return fmt.Errorf("%w: 不支持的防火墙后端", ErrUnsupported)
	}
}

func (s *Service) pingBlocked(ctx context.Context, state backendState) (bool, error) {
	switch state.Name {
	case BackendUFW:
		content, err := os.ReadFile(s.ufwBeforeRules)
		if err != nil {
			return false, err
		}
		for _, line := range strings.Split(string(content), "\n") {
			if strings.Contains(line, "-A ufw-before-input -p icmp --icmp-type echo-request") {
				return strings.Contains(line, "-j DROP"), nil
			}
		}
		return false, fmt.Errorf("未找到 UFW echo-request 规则")
	case BackendFirewalld:
		output, err := s.runner.Run(ctx, "firewall-cmd", "--query-icmp-block=echo-request")
		return err == nil && strings.TrimSpace(string(output)) == "yes", nil
	case BackendIPTables:
		_, err := s.runner.Run(ctx, "iptables", "-C", "INPUT", "-p", "icmp", "--icmp-type", "echo-request", "-m", "comment", "--comment", "oneinstack:ping", "-j", "DROP")
		return err == nil, nil
	default:
		return false, nil
	}
}

func (s *Service) setUFWPingBlocked(ctx context.Context, blocked bool) error {
	original, err := os.ReadFile(s.ufwBeforeRules)
	if err != nil {
		return err
	}
	lines := strings.Split(string(original), "\n")
	found := false
	for index, line := range lines {
		if !strings.Contains(line, "-A ufw-before-input -p icmp --icmp-type echo-request") {
			continue
		}
		found = true
		target := "ACCEPT"
		if blocked {
			target = "DROP"
		}
		if strings.Contains(line, "-j ACCEPT") {
			lines[index] = strings.Replace(line, "-j ACCEPT", "-j "+target, 1)
		} else if strings.Contains(line, "-j DROP") {
			lines[index] = strings.Replace(line, "-j DROP", "-j "+target, 1)
		} else {
			return validationError("UFW echo-request 规则缺少 ACCEPT/DROP 动作")
		}
		break
	}
	if !found {
		return validationError("未找到 UFW echo-request 规则")
	}
	updated := []byte(strings.Join(lines, "\n"))
	if string(updated) == string(original) {
		return nil
	}
	info, err := os.Stat(s.ufwBeforeRules)
	if err != nil {
		return err
	}
	if err := writeAtomic(s.ufwBeforeRules, updated, info.Mode()); err != nil {
		return err
	}
	if _, err := s.runner.Run(ctx, "ufw", "reload"); err != nil {
		_ = writeAtomic(s.ufwBeforeRules, original, info.Mode())
		_, _ = s.runner.Run(ctx, "ufw", "reload")
		return fmt.Errorf("重新加载 UFW 失败，配置已回滚: %w", err)
	}
	return nil
}

func writeAtomic(path string, content []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".oneinstack-firewall-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func oppositeICMPAction(blocked bool) string {
	if blocked {
		return "--remove-icmp-block=echo-request"
	}
	return "--add-icmp-block=echo-request"
}

func appendWarning(current, message string) string {
	if current == "" {
		return message
	}
	return current + "；" + message
}

func (s *Service) loadRuleCounts(counts *output.FirewallRuleCounts) error {
	if counts == nil {
		return nil
	}
	type countRow struct {
		RuleType string
		Total    int64
	}
	var rows []countRow
	if err := s.db.Model(&models.IptablesRule{}).
		Select("COALESCE(NULLIF(rule_type, ''), 'port') AS rule_type, COUNT(*) AS total").
		Group("COALESCE(NULLIF(rule_type, ''), 'port')").
		Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		switch row.RuleType {
		case "port":
			counts.PortRules = row.Total
		case "ip":
			counts.IPRules = row.Total
		case "region":
			counts.RegionRules = row.Total
		case "auto_block":
			counts.AutoBlockRules = row.Total
		}
	}
	return s.db.Model(&models.FirewallPortForward{}).Count(&counts.PortForwards).Error
}

func normalizeStoredRule(rule *models.IptablesRule) {
	if rule.RuleType == "" {
		rule.RuleType = "port"
	}
	if rule.Location == "" {
		rule.Location = describeIPLocation(rule.IPs)
	}
}

func describeIPLocation(raw string) string {
	values := splitValues(raw)
	if len(values) != 1 {
		if len(values) > 1 {
			return "多个地址"
		}
		return "全部 IPv4"
	}
	value := values[0]
	if value == "0.0.0.0/0" {
		return "全部 IPv4"
	}
	host := strings.SplitN(value, "/", 2)[0]
	ip := net.ParseIP(host)
	switch {
	case ip == nil:
		return "未知"
	case ip.IsLoopback():
		return "本机"
	case ip.IsPrivate():
		return "内网"
	default:
		return "公网地址"
	}
}

func uniqueIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	result := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id < 1 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

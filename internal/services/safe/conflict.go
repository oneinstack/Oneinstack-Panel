package safe

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"oneinstack/internal/i18n"
	"oneinstack/internal/models"
)

type firewallCollisionKind string

const (
	ruleDuplicateCollision        firewallCollisionKind = "rule_duplicate"
	ruleStrategyCollision         firewallCollisionKind = "rule_strategy_conflict"
	portForwardDuplicateCollision firewallCollisionKind = "port_forward_duplicate"
	portForwardTargetCollision    firewallCollisionKind = "port_forward_target_conflict"
)

type firewallCollisionError struct {
	kind     firewallCollisionKind
	detailZH string
	detailEN string
}

func (e *firewallCollisionError) Error() string {
	return e.detailZH
}

// Is keeps collision errors compatible with existing validation handling while
// allowing HTTP handlers to return the more precise 409 response below.
func (e *firewallCollisionError) Is(target error) bool {
	return target == ErrValidation
}

// FirewallCollision describes a safe, localized conflict response. Callers can
// expose these fields without returning command output or internal paths.
type FirewallCollision struct {
	StableCode string
	Title      string
	Detail     string
	Field      string
}

// FirewallCollisionInfo extracts collision details through wrapped import,
// batch, preview, and snapshot errors.
func FirewallCollisionInfo(err error, locale string) (FirewallCollision, bool) {
	var collision *firewallCollisionError
	if !errors.As(err, &collision) {
		return FirewallCollision{}, false
	}
	english := i18n.Canonical(locale) == i18n.LocaleEnUS
	result := FirewallCollision{Detail: collision.detailZH}
	if english {
		result.Detail = collision.detailEN
	}
	switch collision.kind {
	case ruleDuplicateCollision:
		result.StableCode = "FIREWALL_RULE_DUPLICATE"
		result.Title = localizedCollisionTitle(english, "防火墙规则已存在", "The firewall rule already exists")
	case ruleStrategyCollision:
		result.StableCode = "FIREWALL_RULE_CONFLICT"
		result.Title = localizedCollisionTitle(english, "防火墙规则与现有规则冲突", "The firewall rule conflicts with an existing rule")
		result.Field = "strategy"
	case portForwardDuplicateCollision:
		result.StableCode = "FIREWALL_PORT_FORWARD_DUPLICATE"
		result.Title = localizedCollisionTitle(english, "端口转发已存在", "The port forwarding rule already exists")
		result.Field = "sourcePort"
	case portForwardTargetCollision:
		result.StableCode = "FIREWALL_PORT_FORWARD_CONFLICT"
		result.Title = localizedCollisionTitle(english, "端口转发与现有规则冲突", "The port forwarding rule conflicts with an existing rule")
		result.Field = "sourcePort"
	default:
		return FirewallCollision{}, false
	}
	return result, true
}

func isRuleDuplicateCollision(err error) bool {
	var collision *firewallCollisionError
	return errors.As(err, &collision) && collision.kind == ruleDuplicateCollision
}

func localizedCollisionTitle(english bool, zhCN, enUS string) string {
	if english {
		return enUS
	}
	return zhCN
}

func newRuleDuplicateError(existing models.IptablesRule) error {
	if existing.Protected {
		return &firewallCollisionError{
			kind: ruleDuplicateCollision,
			detailZH: fmt.Sprintf(
				"相同防火墙规则已存在（规则 ID: %d），且为系统保护规则。系统保护规则不能重复添加、编辑、停用或删除。",
				existing.ID,
			),
			detailEN: fmt.Sprintf(
				"An identical firewall rule already exists (rule ID: %d) and is system-protected. System-protected rules cannot be duplicated, edited, disabled, or deleted.",
				existing.ID,
			),
		}
	}
	return &firewallCollisionError{
		kind: ruleDuplicateCollision,
		detailZH: fmt.Sprintf(
			"相同防火墙规则已存在（规则 ID: %d）。请直接编辑现有规则，不要重复添加。",
			existing.ID,
		),
		detailEN: fmt.Sprintf(
			"An identical firewall rule already exists (rule ID: %d). Edit the existing rule instead of adding a duplicate.",
			existing.ID,
		),
	}
}

func newRuleStrategyConflictError(existing models.IptablesRule, requested normalizedRule) error {
	return &firewallCollisionError{
		kind: ruleStrategyCollision,
		detailZH: fmt.Sprintf(
			"防火墙规则与现有规则冲突（规则 ID: %d）：两条规则的方向、协议、来源和端口范围相同，但现有策略为%s，当前策略为%s。请先停用、编辑或删除现有规则后重试。",
			existing.ID, strategyNameZH(existing.Strategy), strategyNameZH(requested.Strategy),
		),
		detailEN: fmt.Sprintf(
			"The firewall rule conflicts with existing rule ID %d: both rules have the same direction, protocol, source, and port scope, but the existing policy is %s and the requested policy is %s. Disable, edit, or delete the existing rule before retrying.",
			existing.ID, strategyNameEN(existing.Strategy), strategyNameEN(requested.Strategy),
		),
	}
}

func newRuleSetDuplicateError(first, second int) error {
	return &firewallCollisionError{
		kind: ruleDuplicateCollision,
		detailZH: fmt.Sprintf(
			"目标规则集中的第 %d 条与第 %d 条防火墙规则完全相同。请删除重复规则后重试。",
			first, second,
		),
		detailEN: fmt.Sprintf(
			"Firewall rules %d and %d in the target rule set are identical. Remove the duplicate rule and retry.",
			first, second,
		),
	}
}

func newRuleSetStrategyConflictError(first, second int, firstStrategy, secondStrategy string) error {
	return &firewallCollisionError{
		kind: ruleStrategyCollision,
		detailZH: fmt.Sprintf(
			"目标规则集中的第 %d 条与第 %d 条防火墙规则匹配范围相同，但策略分别为%s和%s。请只保留其中一条策略后重试。",
			first, second, strategyNameZH(firstStrategy), strategyNameZH(secondStrategy),
		),
		detailEN: fmt.Sprintf(
			"Firewall rules %d and %d in the target rule set have the same match scope but use %s and %s policies. Keep only one policy and retry.",
			first, second, strategyNameEN(firstStrategy), strategyNameEN(secondStrategy),
		),
	}
}

func newPortForwardDuplicateError(existing models.FirewallPortForward) error {
	return &firewallCollisionError{
		kind: portForwardDuplicateCollision,
		detailZH: fmt.Sprintf(
			"相同端口转发已存在（规则 ID: %d）。请直接编辑现有规则，不要重复添加。",
			existing.ID,
		),
		detailEN: fmt.Sprintf(
			"An identical port forwarding rule already exists (rule ID: %d). Edit the existing rule instead of adding a duplicate.",
			existing.ID,
		),
	}
}

func newPortForwardTargetConflictError(existing, requested models.FirewallPortForward) error {
	return &firewallCollisionError{
		kind: portForwardTargetCollision,
		detailZH: fmt.Sprintf(
			"端口转发与现有规则冲突（规则 ID: %d）：%s/%d 已转发到 %s:%d，不能同时转发到 %s:%d。请先停用、编辑或删除现有规则后重试。",
			existing.ID, existing.Protocol, existing.SourcePort,
			existing.DestinationIP, existing.DestinationPort,
			requested.DestinationIP, requested.DestinationPort,
		),
		detailEN: fmt.Sprintf(
			"The port forwarding rule conflicts with existing rule ID %d: %s/%d already forwards to %s:%d and cannot simultaneously forward to %s:%d. Disable, edit, or delete the existing rule before retrying.",
			existing.ID, existing.Protocol, existing.SourcePort,
			existing.DestinationIP, existing.DestinationPort,
			requested.DestinationIP, requested.DestinationPort,
		),
	}
}

func strategyNameZH(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "allow") {
		return "放行"
	}
	return "拒绝"
}

func strategyNameEN(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "allow") {
		return "Allow"
	}
	return "Deny"
}

func (s *Service) rejectRuleCollision(requested normalizedRule, excludeID int64) error {
	var candidates []models.IptablesRule
	tx := s.db.Order("protected DESC, state DESC, id ASC")
	if excludeID > 0 {
		tx = tx.Where("id <> ?", excludeID)
	}
	if err := tx.Find(&candidates).Error; err != nil {
		return err
	}
	for index := range candidates {
		candidate, err := normalizeRuleForCollision(&candidates[index], s.panelPort)
		if err != nil || !sameRuleMatch(candidate, requested) {
			continue
		}
		if candidate.Strategy == requested.Strategy {
			return newRuleDuplicateError(candidates[index])
		}
		if candidate.State == 1 && requested.State == 1 {
			return newRuleStrategyConflictError(candidates[index], requested)
		}
	}
	return nil
}

func normalizeRuleForCollision(rule *models.IptablesRule, panelPort int) (normalizedRule, error) {
	if rule == nil {
		return normalizedRule{}, validationError("规则不能为空")
	}
	copyOfRule := *rule
	copyOfRule.ExpiresAt = nil
	originalStrategy := strings.ToLower(strings.TrimSpace(copyOfRule.Strategy))
	// Stored legacy deny rules may now violate a newer safety check. They still
	// need to participate in collision detection until they are removed.
	copyOfRule.Strategy = "allow"
	normalized, err := normalizeRule(&copyOfRule, panelPort)
	if err != nil {
		return normalizedRule{}, err
	}
	if originalStrategy != "allow" && originalStrategy != "deny" {
		return normalizedRule{}, validationError("策略必须是 allow 或 deny")
	}
	normalized.Strategy = originalStrategy
	return normalized, nil
}

func sameRuleMatch(left, right normalizedRule) bool {
	if left.Direction != right.Direction || left.Protocol != right.Protocol {
		return false
	}
	if !sameIPv4Set(left.IPs, right.IPs) {
		return false
	}
	if left.Protocol == "tcp" || left.Protocol == "udp" {
		return samePortSet(left.Ports, right.Ports)
	}
	return true
}

type numericRange struct {
	start uint32
	end   uint32
}

func sameIPv4Set(left, right []string) bool {
	leftRanges, leftOK := ipv4Ranges(left)
	rightRanges, rightOK := ipv4Ranges(right)
	return leftOK && rightOK && equalRanges(leftRanges, rightRanges)
}

func ipv4Ranges(values []string) ([]numericRange, bool) {
	ranges := make([]numericRange, 0, len(values))
	for _, value := range values {
		if !strings.Contains(value, "/") {
			ip := net.ParseIP(value).To4()
			if ip == nil {
				return nil, false
			}
			number := binary.BigEndian.Uint32(ip)
			ranges = append(ranges, numericRange{start: number, end: number})
			continue
		}
		ip, network, err := net.ParseCIDR(value)
		if err != nil || ip.To4() == nil {
			return nil, false
		}
		ones, bits := network.Mask.Size()
		if bits != 32 || ones < 0 {
			return nil, false
		}
		start := binary.BigEndian.Uint32(network.IP.To4())
		hostBits := uint(bits - ones)
		end := start
		if hostBits == 32 {
			end = ^uint32(0)
		} else if hostBits > 0 {
			end = start | ((uint32(1) << hostBits) - 1)
		}
		ranges = append(ranges, numericRange{start: start, end: end})
	}
	return mergeRanges(ranges), true
}

func samePortSet(left, right []string) bool {
	return equalRanges(portRanges(left), portRanges(right))
}

func portRanges(values []string) []numericRange {
	if len(values) == 0 {
		return []numericRange{{start: 1, end: 65535}}
	}
	ranges := make([]numericRange, 0, len(values))
	for _, value := range values {
		segments := strings.Split(value, "-")
		start, _ := strconv.ParseUint(segments[0], 10, 32)
		end := start
		if len(segments) == 2 {
			end, _ = strconv.ParseUint(segments[1], 10, 32)
		}
		ranges = append(ranges, numericRange{start: uint32(start), end: uint32(end)})
	}
	return mergeRanges(ranges)
}

func mergeRanges(values []numericRange) []numericRange {
	if len(values) == 0 {
		return nil
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].start == values[j].start {
			return values[i].end < values[j].end
		}
		return values[i].start < values[j].start
	})
	result := make([]numericRange, 0, len(values))
	for _, current := range values {
		if len(result) == 0 {
			result = append(result, current)
			continue
		}
		last := &result[len(result)-1]
		adjacent := last.end != ^uint32(0) && current.start == last.end+1
		if current.start <= last.end || adjacent {
			if current.end > last.end {
				last.end = current.end
			}
			continue
		}
		result = append(result, current)
	}
	return result
}

func equalRanges(left, right []numericRange) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (s *Service) validateRuleSet(rules []models.IptablesRule) error {
	normalized := make([]normalizedRule, len(rules))
	for index := range rules {
		value, err := normalizeRule(&rules[index], s.panelPort)
		if err != nil {
			return fmt.Errorf("目标规则集第 %d 条无效: %w", index+1, err)
		}
		normalized[index] = value
		for previous := 0; previous < index; previous++ {
			if !sameRuleMatch(normalized[previous], value) {
				continue
			}
			if normalized[previous].Strategy == value.Strategy {
				return newRuleSetDuplicateError(previous+1, index+1)
			}
			if normalized[previous].State == 1 && value.State == 1 {
				return newRuleSetStrategyConflictError(
					previous+1, index+1,
					normalized[previous].Strategy, value.Strategy,
				)
			}
		}
	}

	var protected []models.IptablesRule
	if err := s.db.Where("protected = ?", true).Order("id ASC").Find(&protected).Error; err != nil {
		return err
	}
	for index, requested := range normalized {
		for protectedIndex := range protected {
			candidate, err := normalizeRuleForCollision(&protected[protectedIndex], s.panelPort)
			if err != nil || !sameRuleMatch(candidate, requested) {
				continue
			}
			if candidate.Strategy == requested.Strategy {
				return newRuleDuplicateError(protected[protectedIndex])
			}
			if candidate.State == 1 && requested.State == 1 {
				return newRuleStrategyConflictError(protected[protectedIndex], normalized[index])
			}
		}
	}
	return nil
}

func validateStoredRuleCollisions(rules []models.IptablesRule, panelPort int) error {
	normalized := make([]normalizedRule, len(rules))
	for index := range rules {
		value, err := normalizeRuleForCollision(&rules[index], panelPort)
		if err != nil {
			continue
		}
		normalized[index] = value
		for previous := 0; previous < index; previous++ {
			if normalized[previous].Direction == "" || !sameRuleMatch(normalized[previous], value) {
				continue
			}
			if normalized[previous].Strategy == value.Strategy {
				return newRuleDuplicateError(rules[previous])
			}
			if normalized[previous].State == 1 && value.State == 1 {
				return newRuleStrategyConflictError(rules[previous], value)
			}
		}
	}
	return nil
}

func (s *Service) validateActiveRuleCollisions() error {
	if s.db == nil {
		return nil
	}
	var rules []models.IptablesRule
	if err := s.db.Where("state = ?", 1).
		Order("protected DESC, id ASC").Find(&rules).Error; err != nil {
		return err
	}
	return validateStoredRuleCollisions(rules, s.panelPort)
}

func (s *Service) validateActiveCollisions() error {
	if err := s.validateActiveRuleCollisions(); err != nil {
		return err
	}
	return s.validateActivePortForwardCollisions()
}

// ActiveCollision reports legacy rule or port-forward conflicts without
// mutating either entry. The UI can then ask the administrator which one to
// keep.
func (s *Service) ActiveCollision(locale string) (FirewallCollision, bool, error) {
	err := s.validateActiveCollisions()
	if err == nil {
		return FirewallCollision{}, false, nil
	}
	if info, ok := FirewallCollisionInfo(err, locale); ok {
		return info, true, nil
	}
	return FirewallCollision{}, false, err
}

package safe

import (
	"context"
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
	rulePortForwardCollision      firewallCollisionKind = "rule_port_forward_conflict"
	portForwardRuleCollision      firewallCollisionKind = "port_forward_rule_conflict"
	rulePingCollision             firewallCollisionKind = "rule_ping_conflict"
	pingRuleCollision             firewallCollisionKind = "ping_rule_conflict"
	portForwardDuplicateCollision firewallCollisionKind = "port_forward_duplicate"
	portForwardTargetCollision    firewallCollisionKind = "port_forward_target_conflict"
	portForwardProtectedCollision firewallCollisionKind = "port_forward_protected_conflict"
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
	case rulePortForwardCollision:
		result.StableCode = "FIREWALL_RULE_PORT_FORWARD_CONFLICT"
		result.Title = localizedCollisionTitle(english, "防火墙规则与端口转发冲突", "The firewall rule conflicts with port forwarding")
		result.Field = "ports"
	case portForwardRuleCollision:
		result.StableCode = "FIREWALL_PORT_FORWARD_RULE_CONFLICT"
		result.Title = localizedCollisionTitle(english, "端口转发与拒绝规则冲突", "Port forwarding conflicts with a deny rule")
		result.Field = "sourcePort"
	case rulePingCollision:
		result.StableCode = "FIREWALL_RULE_PING_CONFLICT"
		result.Title = localizedCollisionTitle(english, "防火墙规则与 Ping 设置冲突", "The firewall rule conflicts with the Ping setting")
		result.Field = "strategy"
	case pingRuleCollision:
		result.StableCode = "FIREWALL_PING_RULE_CONFLICT"
		result.Title = localizedCollisionTitle(english, "Ping 设置与防火墙规则冲突", "The Ping setting conflicts with a firewall rule")
		result.Field = "blocked"
	case portForwardDuplicateCollision:
		result.StableCode = "FIREWALL_PORT_FORWARD_DUPLICATE"
		result.Title = localizedCollisionTitle(english, "端口转发已存在", "The port forwarding rule already exists")
		result.Field = "sourcePort"
	case portForwardTargetCollision:
		result.StableCode = "FIREWALL_PORT_FORWARD_CONFLICT"
		result.Title = localizedCollisionTitle(english, "端口转发与现有规则冲突", "The port forwarding rule conflicts with an existing rule")
		result.Field = "sourcePort"
	case portForwardProtectedCollision:
		result.StableCode = "FIREWALL_PORT_FORWARD_PROTECTED_PORT_CONFLICT"
		result.Title = localizedCollisionTitle(english, "端口转发占用了受保护端口", "Port forwarding uses a protected port")
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
			"防火墙规则与现有规则冲突（规则 ID: %d）：两条规则方向相同，且协议、IP 和端口的生效范围存在交集，但现有策略为%s，当前策略为%s。请先停用、编辑或删除现有规则，或调整 IP、协议、端口范围以避免重叠后重试。",
			existing.ID, strategyNameZH(existing.Strategy), strategyNameZH(requested.Strategy),
		),
		detailEN: fmt.Sprintf(
			"The firewall rule conflicts with existing rule ID %d: the rules have the same direction and overlapping protocol, IP, and port scopes, but the existing policy is %s and the requested policy is %s. Disable, edit, or delete the existing rule, or adjust its IP, protocol, or port scope to remove the overlap before retrying.",
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
			"目标规则集中的第 %d 条与第 %d 条防火墙规则方向相同，且协议、IP 和端口的生效范围存在交集，但策略分别为%s和%s。请只保留其中一条策略，或调整范围以避免重叠后重试。",
			first, second, strategyNameZH(firstStrategy), strategyNameZH(secondStrategy),
		),
		detailEN: fmt.Sprintf(
			"Firewall rules %d and %d in the target rule set have the same direction and overlapping protocol, IP, and port scopes, but use %s and %s policies. Keep only one policy, or adjust the scopes to remove the overlap before retrying.",
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

func newRulePortForwardConflictError(kind firewallCollisionKind, rule models.IptablesRule, forward models.FirewallPortForward) error {
	ruleRefZH, ruleRefEN := "当前拒绝规则", "the requested deny rule"
	if rule.ID > 0 {
		ruleRefZH = fmt.Sprintf("拒绝规则 ID %d", rule.ID)
		ruleRefEN = fmt.Sprintf("deny rule ID %d", rule.ID)
	}
	forwardRefZH, forwardRefEN := "当前端口转发", "the requested port forwarding rule"
	if forward.ID > 0 {
		forwardRefZH = fmt.Sprintf("端口转发 ID %d", forward.ID)
		forwardRefEN = fmt.Sprintf("port forwarding rule ID %d", forward.ID)
	}
	return &firewallCollisionError{
		kind: kind,
		detailZH: fmt.Sprintf(
			"%s 与%s冲突：端口转发会接受并转发入站 %s/%d 流量，而拒绝规则覆盖了同一流量范围。请停用、编辑或删除其中一项后重试。",
			ruleRefZH, forwardRefZH, strings.ToUpper(forward.Protocol), forward.SourcePort,
		),
		detailEN: fmt.Sprintf(
			"%s conflicts with %s: port forwarding accepts and forwards inbound %s/%d traffic, while the deny rule covers the same traffic scope. Disable, edit, or delete one of them before retrying.",
			ruleRefEN, forwardRefEN, strings.ToUpper(forward.Protocol), forward.SourcePort,
		),
	}
}

func newRulePingConflictError(requested normalizedRule, blocked bool) error {
	return &firewallCollisionError{
		kind: rulePingCollision,
		detailZH: fmt.Sprintf(
			"当前 Ping 响应设置为%s，但该入站 %s 规则的策略为%s，两者覆盖的 ICMP 流量范围存在交集。请调整规则策略/范围，或先修改 Ping 设置后重试。",
			pingPolicyNameZH(blocked), strings.ToUpper(requested.Protocol), strategyNameZH(requested.Strategy),
		),
		detailEN: fmt.Sprintf(
			"Ping responses are currently %s, but this inbound %s rule uses the %s policy and overlaps the same ICMP traffic scope. Adjust the rule policy or scope, or change the Ping setting before retrying.",
			pingPolicyNameEN(blocked), strings.ToUpper(requested.Protocol), strategyNameEN(requested.Strategy),
		),
	}
}

func newPingRuleConflictError(existing models.IptablesRule, blocked bool) error {
	return &firewallCollisionError{
		kind: pingRuleCollision,
		detailZH: fmt.Sprintf(
			"Ping 响应不能设置为%s：现有入站规则 ID %d 使用%s策略，且覆盖 ICMP 流量。请先停用、编辑或删除该规则后重试。",
			pingPolicyNameZH(blocked), existing.ID, strategyNameZH(existing.Strategy),
		),
		detailEN: fmt.Sprintf(
			"Ping responses cannot be set to %s: existing inbound rule ID %d uses the %s policy and covers ICMP traffic. Disable, edit, or delete that rule before retrying.",
			pingPolicyNameEN(blocked), existing.ID, strategyNameEN(existing.Strategy),
		),
	}
}

func newPortForwardProtectedConflictError(forward models.FirewallPortForward, protectedRuleID int64) error {
	protectedRefZH, protectedRefEN := "面板管理端口", "a Panel management port"
	if protectedRuleID > 0 {
		protectedRefZH = fmt.Sprintf("系统保护规则 ID %d 对应的面板管理端口", protectedRuleID)
		protectedRefEN = fmt.Sprintf("the Panel management port protected by system rule ID %d", protectedRuleID)
	}
	return &firewallCollisionError{
		kind: portForwardProtectedCollision,
		detailZH: fmt.Sprintf(
			"不能将 %s/%d 用作端口转发源端口：该端口是%s，转发后可能导致面板无法访问。请更换源端口后重试。",
			strings.ToUpper(forward.Protocol), forward.SourcePort, protectedRefZH,
		),
		detailEN: fmt.Sprintf(
			"%s/%d cannot be used as a port forwarding source because it is %s. Forwarding it may make the Panel unreachable. Choose another source port and retry.",
			strings.ToUpper(forward.Protocol), forward.SourcePort, protectedRefEN,
		),
	}
}

func newProtectedPortForwardConflictError(port int, forward models.FirewallPortForward) error {
	return &firewallCollisionError{
		kind: portForwardProtectedCollision,
		detailZH: fmt.Sprintf(
			"不能保护面板管理端口 TCP/%d：现有端口转发 ID %d 已占用该源端口并转发到 %s:%d。请先停用、编辑或删除该端口转发。",
			port, forward.ID, forward.DestinationIP, forward.DestinationPort,
		),
		detailEN: fmt.Sprintf(
			"Panel management port TCP/%d cannot be protected because existing port forwarding rule ID %d already uses that source port and forwards it to %s:%d. Disable, edit, or delete that forwarding rule first.",
			port, forward.ID, forward.DestinationIP, forward.DestinationPort,
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

func pingPolicyNameZH(blocked bool) string {
	if blocked {
		return "阻止状态"
	}
	return "允许状态"
}

func pingPolicyNameEN(blocked bool) string {
	if blocked {
		return "blocked"
	}
	return "allowed"
}

func (s *Service) rejectRuleCollision(ctx context.Context, requested normalizedRule, excludeID int64) error {
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
		if err != nil {
			continue
		}
		if candidate.Strategy == requested.Strategy && sameRuleMatch(candidate, requested) {
			return newRuleDuplicateError(candidates[index])
		}
		if conflictingRuleScopes(candidate, requested) {
			return newRuleStrategyConflictError(candidates[index], requested)
		}
	}
	if err := s.rejectRulePortForwardCollision(requested); err != nil {
		return err
	}
	return s.rejectRulePingCollision(ctx, requested)
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

func ruleScopesOverlap(left, right normalizedRule) bool {
	if left.Direction != right.Direction || !protocolsOverlap(left.Protocol, right.Protocol) {
		return false
	}
	if !ipv4SetsOverlap(left.IPs, right.IPs) {
		return false
	}
	if left.Protocol == "all" || right.Protocol == "all" {
		return true
	}
	if left.Protocol == "tcp" || left.Protocol == "udp" {
		return rangesOverlap(portRanges(left.Ports), portRanges(right.Ports))
	}
	return true
}

func conflictingRuleScopes(left, right normalizedRule) bool {
	if left.RuleType == "auto_block" || right.RuleType == "auto_block" {
		return false
	}
	return left.Strategy != right.Strategy && left.State == 1 && right.State == 1 &&
		ruleScopesOverlap(left, right)
}

func protocolsOverlap(left, right string) bool {
	return left == right || left == "all" || right == "all"
}

func normalizedPortForwardScope(forward models.FirewallPortForward) normalizedRule {
	return normalizedRule{
		Direction: "in",
		Protocol:  forward.Protocol,
		Strategy:  "allow",
		IPs:       []string{"0.0.0.0/0"},
		Ports:     []string{strconv.Itoa(forward.SourcePort)},
		State:     forward.State,
	}
}

func (s *Service) rejectRulePortForwardCollision(requested normalizedRule) error {
	if s.db == nil || requested.RuleType == "auto_block" || requested.State != 1 || requested.Direction != "in" || requested.Strategy != "deny" ||
		(requested.Protocol != "tcp" && requested.Protocol != "udp" && requested.Protocol != "all") {
		return nil
	}
	var forwards []models.FirewallPortForward
	if err := s.db.Where("state = ?", 1).Order("id ASC").Find(&forwards).Error; err != nil {
		return err
	}
	for index := range forwards {
		forward := forwards[index]
		if err := s.normalizePortForward(&forward); err != nil {
			continue
		}
		if ruleScopesOverlap(requested, normalizedPortForwardScope(forward)) {
			return newRulePortForwardConflictError(rulePortForwardCollision, models.IptablesRule{Strategy: requested.Strategy}, forwards[index])
		}
	}
	return nil
}

func (s *Service) rejectPortForwardRuleCollision(requested models.FirewallPortForward) error {
	if s.db == nil || requested.State != 1 {
		return nil
	}
	forwardScope := normalizedPortForwardScope(requested)
	var rules []models.IptablesRule
	if err := s.db.Where("state = ?", 1).Order("protected DESC, id ASC").Find(&rules).Error; err != nil {
		return err
	}
	for index := range rules {
		candidate, err := normalizeRuleForCollision(&rules[index], s.panelPort)
		if err != nil {
			continue
		}
		if candidate.RuleType != "auto_block" && candidate.Direction == "in" && candidate.Strategy == "deny" &&
			ruleScopesOverlap(candidate, forwardScope) {
			return newRulePortForwardConflictError(portForwardRuleCollision, rules[index], requested)
		}
	}
	return nil
}

func (s *Service) rejectPortForwardProtectedCollision(requested models.FirewallPortForward) error {
	if requested.Protocol != "tcp" {
		return nil
	}
	if s.db != nil {
		var protected []models.IptablesRule
		if err := s.db.Where("protected = ? AND state = ?", true, 1).
			Order("id ASC").Find(&protected).Error; err != nil {
			return err
		}
		for index := range protected {
			candidate, err := normalizeRuleForCollision(&protected[index], s.panelPort)
			if err != nil || candidate.Direction != "in" ||
				(candidate.Protocol != "tcp" && candidate.Protocol != "all") ||
				!portSetContains(candidate.Ports, requested.SourcePort) {
				continue
			}
			return newPortForwardProtectedConflictError(requested, protected[index].ID)
		}
	}
	if requested.SourcePort == s.panelPort {
		return newPortForwardProtectedConflictError(requested, 0)
	}
	return nil
}

func (s *Service) rejectProtectedPortForwardCollision(port int) error {
	if s.db == nil {
		return nil
	}
	var forwards []models.FirewallPortForward
	if err := s.db.Where("state = ?", 1).Order("id ASC").Find(&forwards).Error; err != nil {
		return err
	}
	for index := range forwards {
		candidate := forwards[index]
		if err := s.normalizePortForward(&candidate); err != nil ||
			candidate.Protocol != "tcp" || candidate.SourcePort != port {
			continue
		}
		return newProtectedPortForwardConflictError(port, forwards[index])
	}
	return nil
}

func ruleAffectsInboundICMP(rule normalizedRule) bool {
	return rule.RuleType != "auto_block" && rule.State == 1 && rule.Direction == "in" &&
		(rule.Protocol == "icmp" || rule.Protocol == "all")
}

func pingStrategy(blocked bool) string {
	if blocked {
		return "deny"
	}
	return "allow"
}

func (s *Service) rejectRulePingCollision(ctx context.Context, requested normalizedRule) error {
	if !ruleAffectsInboundICMP(requested) {
		return nil
	}
	state := s.detectBackend(ctx)
	if !state.Installed || state.Name == BackendNone {
		return nil
	}
	blocked, err := s.collisionPingBlocked(ctx, state)
	if err != nil {
		// Do not make all rule changes unavailable merely because the host's Ping
		// state cannot be read. Status continues to surface that probe failure.
		return nil
	}
	if requested.Strategy != pingStrategy(blocked) {
		return newRulePingConflictError(requested, blocked)
	}
	return nil
}

func (s *Service) rejectPingRuleCollision(blocked bool) error {
	if s.db == nil {
		return nil
	}
	var rules []models.IptablesRule
	if err := s.db.Where("state = ?", 1).Order("protected DESC, id ASC").Find(&rules).Error; err != nil {
		return err
	}
	for index := range rules {
		candidate, err := normalizeRuleForCollision(&rules[index], s.panelPort)
		if err != nil || !ruleAffectsInboundICMP(candidate) {
			continue
		}
		if candidate.Strategy != pingStrategy(blocked) {
			return newPingRuleConflictError(rules[index], blocked)
		}
	}
	return nil
}

func (s *Service) collisionPingBlocked(ctx context.Context, state backendState) (bool, error) {
	if state.Name == BackendFirewalld && !state.Enabled {
		if _, err := s.runner.LookPath("firewall-offline-cmd"); err != nil {
			return false, err
		}
		output, err := s.runner.Run(ctx, "firewall-offline-cmd", "--query-icmp-block=echo-request")
		return err == nil && strings.TrimSpace(string(output)) == "yes", nil
	}
	return s.pingBlocked(ctx, state)
}

// ValidatePingSetting checks the requested global Ping policy without changing
// the host firewall. Preview and execution use the same collision rule.
func (s *Service) ValidatePingSetting(blocked bool) error {
	return s.rejectPingRuleCollision(blocked)
}

// ValidateActiveConfiguration checks all currently enabled managed firewall
// features without applying, deleting, or reordering any host rule.
func (s *Service) ValidateActiveConfiguration(ctx context.Context) error {
	return s.validateActiveCollisions(ctx)
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

func ipv4SetsOverlap(left, right []string) bool {
	leftRanges, leftOK := ipv4Ranges(left)
	rightRanges, rightOK := ipv4Ranges(right)
	return leftOK && rightOK && rangesOverlap(leftRanges, rightRanges)
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

func rangesOverlap(left, right []numericRange) bool {
	leftIndex, rightIndex := 0, 0
	for leftIndex < len(left) && rightIndex < len(right) {
		leftRange := left[leftIndex]
		rightRange := right[rightIndex]
		if leftRange.end < rightRange.start {
			leftIndex++
			continue
		}
		if rightRange.end < leftRange.start {
			rightIndex++
			continue
		}
		return true
	}
	return false
}

func (s *Service) validateRuleSet(ctx context.Context, rules []models.IptablesRule) error {
	normalized := make([]normalizedRule, len(rules))
	for index := range rules {
		value, err := normalizeRule(&rules[index], s.panelPort)
		if err != nil {
			return fmt.Errorf("目标规则集第 %d 条无效: %w", index+1, err)
		}
		normalized[index] = value
		for previous := 0; previous < index; previous++ {
			if normalized[previous].Strategy == value.Strategy &&
				sameRuleMatch(normalized[previous], value) {
				return newRuleSetDuplicateError(previous+1, index+1)
			}
			if conflictingRuleScopes(normalized[previous], value) {
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
			if err != nil {
				continue
			}
			if candidate.Strategy == requested.Strategy && sameRuleMatch(candidate, requested) {
				return newRuleDuplicateError(protected[protectedIndex])
			}
			if conflictingRuleScopes(candidate, requested) {
				return newRuleStrategyConflictError(protected[protectedIndex], normalized[index])
			}
		}
		if err := s.rejectRulePortForwardCollision(requested); err != nil {
			return err
		}
		if err := s.rejectRulePingCollision(ctx, requested); err != nil {
			return err
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
			if normalized[previous].Direction == "" {
				continue
			}
			if normalized[previous].Strategy == value.Strategy &&
				sameRuleMatch(normalized[previous], value) {
				return newRuleDuplicateError(rules[previous])
			}
			if conflictingRuleScopes(normalized[previous], value) {
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

func (s *Service) validateActiveCollisions(ctx context.Context) error {
	if err := s.validateActiveRuleCollisions(); err != nil {
		return err
	}
	if err := s.validateActivePortForwardCollisions(); err != nil {
		return err
	}
	state := s.detectBackend(ctx)
	if !state.Installed || state.Name == BackendNone {
		return nil
	}
	blocked, err := s.collisionPingBlocked(ctx, state)
	if err != nil {
		return nil
	}
	return s.rejectPingRuleCollision(blocked)
}

// ActiveCollision reports legacy rule or port-forward conflicts without
// mutating either entry. The UI can then ask the administrator which one to
// keep.
func (s *Service) ActiveCollision(ctx context.Context, locale string) (FirewallCollision, bool, error) {
	err := s.validateActiveCollisions(ctx)
	if err == nil {
		return FirewallCollision{}, false, nil
	}
	if info, ok := FirewallCollisionInfo(err, locale); ok {
		return info, true, nil
	}
	return FirewallCollision{}, false, err
}

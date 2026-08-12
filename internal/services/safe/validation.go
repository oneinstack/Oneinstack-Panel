package safe

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"oneinstack/internal/models"
)

type normalizedRule struct {
	RuleType  string
	Direction string
	Protocol  string
	Strategy  string
	IPs       []string
	Ports     []string
	Remark    string
	Location  string
	ExpiresAt *time.Time
	State     int
}

func normalizeRule(rule *models.IptablesRule, panelPort int) (normalizedRule, error) {
	if rule == nil {
		return normalizedRule{}, validationError("规则不能为空")
	}
	result := normalizedRule{
		RuleType:  strings.ToLower(strings.TrimSpace(rule.RuleType)),
		Direction: strings.ToLower(strings.TrimSpace(rule.Direction)),
		Protocol:  strings.ToLower(strings.TrimSpace(rule.Protocol)),
		Strategy:  strings.ToLower(strings.TrimSpace(rule.Strategy)),
		Remark:    strings.TrimSpace(rule.Remark),
		Location:  strings.TrimSpace(rule.Location),
		ExpiresAt: rule.ExpiresAt,
	}
	if result.RuleType == "" {
		result.RuleType = "port"
	}
	switch result.RuleType {
	case "port", "ip", "region", "auto_block":
	default:
		return normalizedRule{}, validationError("规则类型必须是 port、ip、region 或 auto_block")
	}
	if rule.State != 0 {
		result.State = 1
	}
	if result.RuleType != "port" {
		result.Direction = "in"
		result.Protocol = "all"
		rule.Ports = ""
	}
	if result.Direction != "in" && result.Direction != "out" {
		return normalizedRule{}, validationError("规则方向必须是 in 或 out")
	}
	if result.Protocol != "tcp" && result.Protocol != "udp" && result.Protocol != "icmp" && result.Protocol != "all" {
		return normalizedRule{}, validationError("协议必须是 tcp、udp、icmp 或 all")
	}
	if result.Strategy != "allow" && result.Strategy != "deny" {
		return normalizedRule{}, validationError("策略必须是 allow 或 deny")
	}
	if utf8.RuneCountInString(result.Remark) > 200 {
		return normalizedRule{}, validationError("备注不能超过 200 个字符")
	}
	if utf8.RuneCountInString(result.Location) > 128 {
		return normalizedRule{}, validationError("IP 归属地不能超过 128 个字符")
	}
	if result.RuleType == "region" && result.Location == "" {
		return normalizedRule{}, validationError("地区规则必须填写地区名称")
	}
	if result.ExpiresAt != nil && !result.ExpiresAt.After(time.Now()) {
		return normalizedRule{}, validationError("过期时间必须晚于当前时间")
	}

	var err error
	result.IPs, err = normalizeIPs(rule.IPs)
	if err != nil {
		return normalizedRule{}, err
	}
	result.Ports, err = normalizePorts(rule.Ports, result.Protocol)
	if err != nil {
		return normalizedRule{}, err
	}
	if len(result.IPs)*max(1, len(result.Ports)) > 100 {
		return normalizedRule{}, validationError("单条规则展开后不能超过 100 个 IP/端口组合")
	}
	if result.Direction == "in" && result.Strategy == "deny" &&
		result.Protocol == "all" && containsAllIPv4(result.IPs) {
		return normalizedRule{}, validationError("不能创建会阻断全部 IPv4 入站流量的规则")
	}
	if result.Direction == "in" && result.Strategy == "deny" &&
		(result.Protocol == "tcp" || result.Protocol == "udp") &&
		portSetContains(result.Ports, panelPort) {
		return normalizedRule{}, validationError(fmt.Sprintf("不能创建会封禁面板端口 %d 的入站规则", panelPort))
	}
	return result, nil
}

func normalizeIPs(raw string) ([]string, error) {
	parts := splitValues(raw)
	if len(parts) == 0 {
		return []string{"0.0.0.0/0"}, nil
	}
	seen := make(map[string]struct{}, len(parts))
	result := make([]string, 0, len(parts))
	for _, value := range parts {
		if strings.EqualFold(value, "any") || value == "0.0.0.0/0" {
			value = "0.0.0.0/0"
		} else if strings.Contains(value, "/") {
			ip, network, err := net.ParseCIDR(value)
			if err != nil || ip.To4() == nil {
				return nil, validationError("IP 仅支持 IPv4 地址或 CIDR 网段")
			}
			value = network.String()
		} else {
			ip := net.ParseIP(value)
			if ip == nil || ip.To4() == nil {
				return nil, validationError("IP 仅支持 IPv4 地址或 CIDR 网段")
			}
			value = ip.To4().String()
		}
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result, nil
}

func normalizePorts(raw, protocol string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if protocol == "icmp" || protocol == "all" {
		return nil, validationError("ICMP 或全协议规则不能指定端口")
	}
	parts := strings.Split(raw, ",")
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
		if parts[index] == "" {
			return nil, validationError("端口列表不能包含空项")
		}
	}
	seen := make(map[string]struct{}, len(parts))
	result := make([]string, 0, len(parts))
	for _, value := range parts {
		segments := strings.Split(value, "-")
		if len(segments) > 2 {
			return nil, validationError("端口格式必须是单个端口或 start-end")
		}
		start, err := strconv.Atoi(strings.TrimSpace(segments[0]))
		if err != nil || start < 1 || start > 65535 {
			return nil, validationError("端口必须在 1-65535 之间")
		}
		end := start
		if len(segments) == 2 {
			end, err = strconv.Atoi(strings.TrimSpace(segments[1]))
			if err != nil || end < start || end > 65535 {
				return nil, validationError("端口范围无效")
			}
		}
		canonical := strconv.Itoa(start)
		if end != start {
			canonical += "-" + strconv.Itoa(end)
		}
		if _, ok := seen[canonical]; !ok {
			seen[canonical] = struct{}{}
			result = append(result, canonical)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		left, _ := strconv.Atoi(strings.Split(result[i], "-")[0])
		right, _ := strconv.Atoi(strings.Split(result[j], "-")[0])
		return left < right
	})
	return result, nil
}

// ValidateRule applies the same validation used by add, update, import, and
// execution without mutating the caller's rule or changing system state.
func (s *Service) ValidateRule(rule *models.IptablesRule) error {
	if rule == nil {
		return validationError("规则不能为空")
	}
	copyOfRule := *rule
	_, err := normalizeRule(&copyOfRule, s.panelPort)
	return err
}

func splitValues(raw string) []string {
	values := strings.Split(raw, ",")
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func portSetContains(ports []string, port int) bool {
	if len(ports) == 0 {
		return true
	}
	for _, value := range ports {
		segments := strings.Split(value, "-")
		start, _ := strconv.Atoi(segments[0])
		end := start
		if len(segments) == 2 {
			end, _ = strconv.Atoi(segments[1])
		}
		if port >= start && port <= end {
			return true
		}
	}
	return false
}

func validationError(message string) error {
	return fmt.Errorf("%w: %s", ErrValidation, message)
}

// ValidationMessage returns only the safe business reason from a wrapped
// validation error, without exposing the internal sentinel text.
func ValidationMessage(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	marker := ErrValidation.Error() + ":"
	if index := strings.LastIndex(message, marker); index >= 0 {
		message = strings.TrimSpace(message[index+len(marker):])
	}
	return message
}

func applyNormalized(rule *models.IptablesRule, normalized normalizedRule) {
	rule.RuleType = normalized.RuleType
	rule.Direction = normalized.Direction
	rule.Protocol = normalized.Protocol
	rule.Strategy = normalized.Strategy
	rule.IPs = strings.Join(normalized.IPs, ",")
	rule.Ports = strings.Join(normalized.Ports, ",")
	rule.Remark = normalized.Remark
	rule.Location = normalized.Location
	rule.ExpiresAt = normalized.ExpiresAt
	rule.State = normalized.State
}

func containsAllIPv4(ips []string) bool {
	for _, value := range ips {
		if value == "0.0.0.0/0" {
			return true
		}
	}
	return false
}

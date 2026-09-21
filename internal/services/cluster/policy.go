package cluster

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"oneinstack/internal/models"

	"gorm.io/gorm"
)

var serviceUnitPattern = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,112}(?:\.service)?$`)

type PolicyInput struct {
	CPUWarningThreshold     float64  `json:"cpuWarningThreshold"`
	CPUCriticalThreshold    float64  `json:"cpuCriticalThreshold"`
	MemoryWarningThreshold  float64  `json:"memoryWarningThreshold"`
	MemoryCriticalThreshold float64  `json:"memoryCriticalThreshold"`
	DiskWarningThreshold    float64  `json:"diskWarningThreshold"`
	DiskCriticalThreshold   float64  `json:"diskCriticalThreshold"`
	DNSTargets              []string `json:"dnsTargets"`
	NetworkTargets          []string `json:"networkTargets"`
	CommonPorts             []int    `json:"commonPorts"`
	ExtraServices           []string `json:"extraServices"`
	LogLookbackMinutes      int      `json:"logLookbackMinutes"`
	MaxLogEntries           int      `json:"maxLogEntries"`
	DiagnosisConcurrency    int      `json:"diagnosisConcurrency"`
	LifecycleConcurrency    int      `json:"lifecycleConcurrency"`
	RestartConcurrency      int      `json:"restartConcurrency"`
	UpdateConcurrency       int      `json:"updateConcurrency"`
}

func defaultClusterPolicy() models.ClusterPolicy {
	return models.ClusterPolicy{
		ID:                  1,
		CPUWarningThreshold: 80, CPUCriticalThreshold: 90,
		MemoryWarningThreshold: 80, MemoryCriticalThreshold: 90,
		DiskWarningThreshold: 80, DiskCriticalThreshold: 90,
		DNSTargets: []string{}, NetworkTargets: []string{}, CommonPorts: []int{22}, ExtraServices: []string{},
		LogLookbackMinutes: 30, MaxLogEntries: 200,
		DiagnosisConcurrency: 10, LifecycleConcurrency: 10,
		RestartConcurrency: 3, UpdateConcurrency: 1,
	}
}

func (m *Manager) GetPolicy() (models.ClusterPolicy, error) {
	if !m.db.Migrator().HasTable(&models.ClusterPolicy{}) {
		return defaultClusterPolicy(), nil
	}
	var policy models.ClusterPolicy
	err := m.db.First(&policy, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		policy = defaultClusterPolicy()
		if err = m.db.Create(&policy).Error; err != nil {
			return models.ClusterPolicy{}, err
		}
		return policy, nil
	}
	policy.DNSTargets = nonNilStrings(policy.DNSTargets)
	policy.NetworkTargets = nonNilStrings(policy.NetworkTargets)
	policy.CommonPorts = nonNilInts(policy.CommonPorts)
	policy.ExtraServices = nonNilStrings(policy.ExtraServices)
	return policy, err
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nonNilInts(values []int) []int {
	if values == nil {
		return []int{}
	}
	return values
}

func (m *Manager) UpdatePolicy(input PolicyInput) (models.ClusterPolicy, error) {
	if err := validateThresholds(input); err != nil {
		return models.ClusterPolicy{}, err
	}
	dnsTargets, err := normalizeTargets(input.DNSTargets, 10)
	if err != nil {
		return models.ClusterPolicy{}, err
	}
	networkTargets, err := normalizeTargets(input.NetworkTargets, 10)
	if err != nil {
		return models.ClusterPolicy{}, err
	}
	ports, err := normalizePorts(input.CommonPorts)
	if err != nil {
		return models.ClusterPolicy{}, err
	}
	services, err := normalizeServices(input.ExtraServices)
	if err != nil {
		return models.ClusterPolicy{}, err
	}
	if input.LogLookbackMinutes < 1 || input.LogLookbackMinutes > 1440 {
		return models.ClusterPolicy{}, errors.New("日志时间窗口必须在 1 到 1440 分钟之间")
	}
	if input.MaxLogEntries < 10 || input.MaxLogEntries > 1000 {
		return models.ClusterPolicy{}, errors.New("日志条数必须在 10 到 1000 之间")
	}
	if !validConcurrency(input.DiagnosisConcurrency, 20) ||
		!validConcurrency(input.LifecycleConcurrency, 20) ||
		!validConcurrency(input.RestartConcurrency, 10) ||
		!validConcurrency(input.UpdateConcurrency, 5) {
		return models.ClusterPolicy{}, errors.New("批量并发数超出允许范围")
	}
	policy := models.ClusterPolicy{
		ID:                  1,
		CPUWarningThreshold: input.CPUWarningThreshold, CPUCriticalThreshold: input.CPUCriticalThreshold,
		MemoryWarningThreshold: input.MemoryWarningThreshold, MemoryCriticalThreshold: input.MemoryCriticalThreshold,
		DiskWarningThreshold: input.DiskWarningThreshold, DiskCriticalThreshold: input.DiskCriticalThreshold,
		DNSTargets: dnsTargets, NetworkTargets: networkTargets,
		CommonPorts: ports, ExtraServices: services,
		LogLookbackMinutes: input.LogLookbackMinutes, MaxLogEntries: input.MaxLogEntries,
		DiagnosisConcurrency: input.DiagnosisConcurrency, LifecycleConcurrency: input.LifecycleConcurrency,
		RestartConcurrency: input.RestartConcurrency, UpdateConcurrency: input.UpdateConcurrency,
	}
	if err := m.db.Save(&policy).Error; err != nil {
		return models.ClusterPolicy{}, err
	}
	return policy, nil
}

func validateThresholds(input PolicyInput) error {
	pairs := [][2]float64{
		{input.CPUWarningThreshold, input.CPUCriticalThreshold},
		{input.MemoryWarningThreshold, input.MemoryCriticalThreshold},
		{input.DiskWarningThreshold, input.DiskCriticalThreshold},
	}
	for _, pair := range pairs {
		if pair[0] < 0 || pair[1] > 100 || pair[0] >= pair[1] {
			return errors.New("指标阈值必须满足 0 <= 警告阈值 < 严重阈值 <= 100")
		}
	}
	return nil
}

func validConcurrency(value, maximum int) bool { return value >= 1 && value <= maximum }

func normalizeTargets(values []string, maximum int) ([]string, error) {
	if len(values) > maximum {
		return nil, errors.New("诊断目标数量超过限制")
	}
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		target := raw
		if parsed, err := url.Parse(raw); err == nil && parsed.Hostname() != "" {
			target = parsed.Hostname()
		}
		if len(target) > 253 || (net.ParseIP(target) == nil && !validHostname(target)) {
			return nil, errors.New("诊断目标必须是有效的 IP、域名或 URL")
		}
		target = strings.ToLower(strings.TrimSuffix(target, "."))
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		result = append(result, target)
	}
	return result, nil
}

func validHostname(value string) bool {
	if value == "" || strings.ContainsAny(value, " /\\\t\r\n") {
		return false
	}
	for _, part := range strings.Split(value, ".") {
		if part == "" || len(part) > 63 || strings.HasPrefix(part, "-") || strings.HasSuffix(part, "-") {
			return false
		}
		for _, char := range part {
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

func normalizePorts(values []int) ([]int, error) {
	if len(values) > 32 {
		return nil, errors.New("常用端口数量不能超过 32")
	}
	seen := map[int]struct{}{}
	result := make([]int, 0, len(values))
	for _, port := range values {
		if port < 1 || port > 65535 {
			return nil, errors.New("端口必须在 1 到 65535 之间")
		}
		if _, ok := seen[port]; ok {
			continue
		}
		seen[port] = struct{}{}
		result = append(result, port)
	}
	sort.Ints(result)
	return result, nil
}

func normalizeServices(values []string) ([]string, error) {
	if len(values) > 32 {
		return nil, errors.New("额外服务数量不能超过 32")
	}
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !serviceUnitPattern.MatchString(value) {
			return nil, errors.New("服务名称格式无效")
		}
		if !strings.HasSuffix(value, ".service") {
			value += ".service"
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func (m *Manager) enrichNode(node *models.ClusterNode) error {
	policy, err := m.GetPolicy()
	if err != nil {
		return err
	}
	node.ConnectionStatus = node.Status
	if node.LifecycleStatus == "" {
		node.LifecycleStatus = models.ClusterNodeLifecycleActive
	}
	// Compatibility for nodes disabled before lifecycle state was introduced.
	if !node.Enabled && node.LifecycleStatus == models.ClusterNodeLifecycleActive {
		node.LifecycleStatus = models.ClusterNodeLifecycleDisabled
	}
	node.EffectiveStatus = effectiveNodeStatus(*node)
	node.EndpointAddressMismatch, node.EndpointAddressNote = analyzeEndpointAddress(*node)
	node.MetricHealth = metricHealth(*node, policy)
	return nil
}

// analyzeEndpointAddress checks if the endpoint IP differs from the node's
// reported IP address. For cloud VMs this is common: the endpoint may use a
// public IP while the node reports its private interface IP. Returns whether
// there's a mismatch and an explanatory note.
func analyzeEndpointAddress(node models.ClusterNode) (bool, string) {
	configured := net.ParseIP(strings.TrimSpace(hostFromEndpoint(node.Endpoint)))
	reported := net.ParseIP(strings.TrimSpace(node.IPAddress))

	// No mismatch if endpoint is a hostname (not an IP) or node hasn't reported yet
	if configured == nil || reported == nil {
		return false, ""
	}

	// IPs match - no issue
	if configured.Equal(reported) {
		return false, ""
	}

	// Check if this looks like a public/private IP NAT scenario
	configuredPrivate := isPrivateIP(configured)
	reportedPrivate := isPrivateIP(reported)

	if !configuredPrivate && reportedPrivate {
		// Endpoint uses public IP, node reports private IP - typical cloud NAT
		if node.Status == models.ClusterNodeStatusOnline {
			return false, "节点报告私有 IP 而端点使用公网 IP，但心跳正常，NAT 配置有效。"
		}
		return true, "节点报告私有 IP 而端点使用公网 IP，且节点离线。请检查 NAT、安全组和防火墙配置。"
	}

	if configuredPrivate && !reportedPrivate {
		// Endpoint uses private IP, node reports public IP - unusual but possible
		if node.Status == models.ClusterNodeStatusOnline {
			return false, "端点配置为私有 IP 而节点报告公网 IP，但心跳正常。"
		}
		return true, "端点配置为私有 IP 而节点报告公网 IP，且节点离线。请确认网络可达性。"
	}

	// Both are private or both are public but different
	if node.Status == models.ClusterNodeStatusOnline {
		return false, "端点 IP 与节点报告 IP 不同，但心跳正常。可能有多网卡或代理配置。"
	}
	return true, "端点 IP 与节点报告 IP 不同，且节点离线。请检查端点地址配置。"
}

// isPrivateIP checks if an IP address is in a private range (RFC 1918, RFC 4193)
func isPrivateIP(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		// 10.0.0.0/8
		if ip4[0] == 10 {
			return true
		}
		// 172.16.0.0/12
		if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
			return true
		}
		// 192.168.0.0/16
		if ip4[0] == 192 && ip4[1] == 168 {
			return true
		}
		// 100.64.0.0/10 (Carrier-grade NAT)
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return true
		}
		return false
	}
	// IPv6 unique local (fc00::/7)
	if len(ip) == net.IPv6len && (ip[0]&0xfe) == 0xfc {
		return true
	}
	return false
}

func hostFromEndpoint(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func effectiveNodeStatus(node models.ClusterNode) string {
	switch node.LifecycleStatus {
	case models.ClusterNodeLifecyclePendingDelete,
		models.ClusterNodeLifecycleDisabled,
		models.ClusterNodeLifecycleDraining,
		models.ClusterNodeLifecycleDrained,
		models.ClusterNodeLifecycleMaintenance:
		return node.LifecycleStatus
	default:
		return node.Status
	}
}

func metricHealth(node models.ClusterNode, policy models.ClusterPolicy) models.ClusterMetricHealth {
	online := node.Status == models.ClusterNodeStatusOnline
	return models.ClusterMetricHealth{
		CPU:    metricLevel(node.CPUPercent, policy.CPUWarningThreshold, policy.CPUCriticalThreshold, online && node.CPUTotalCores > 0),
		Memory: metricLevel(node.MemoryPercent, policy.MemoryWarningThreshold, policy.MemoryCriticalThreshold, online && node.MemoryTotalBytes > 0),
		Disk:   metricLevel(node.DiskPercent, policy.DiskWarningThreshold, policy.DiskCriticalThreshold, online && node.DiskTotalBytes > 0),
	}
}

func metricLevel(value, warning, critical float64, available bool) models.ClusterMetricLevel {
	level := "unknown"
	if available {
		level = "normal"
		if value >= critical {
			level = "critical"
		} else if value >= warning {
			level = "warning"
		}
	}
	return models.ClusterMetricLevel{Value: value, Level: level, WarningThreshold: warning, CriticalThreshold: critical}
}

func ApplyControllerPolicy(node *ControllerNode, policy models.ClusterPolicy) {
	if node == nil {
		return
	}
	node.ConnectionStatus = node.Status
	node.LifecycleStatus = models.ClusterNodeLifecycleActive
	node.EffectiveStatus = node.Status
	proxy := models.ClusterNode{
		Status: node.Status, CPUPercent: node.CPUPercent, MemoryPercent: node.MemoryPercent, DiskPercent: node.DiskPercent,
		CPUTotalCores: node.CPUTotalCores, MemoryTotalBytes: node.MemoryTotalBytes, DiskTotalBytes: node.DiskTotalBytes,
	}
	node.MetricHealth = metricHealth(proxy, policy)
}

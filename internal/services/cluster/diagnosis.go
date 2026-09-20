package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services/monitoring"

	"github.com/shirou/gopsutil/v4/disk"
)

const (
	TaskNodeDiagnose       = "node.diagnose.v1"
	CapabilityNodeDiagnose = "node.diagnose.v1"
	CapabilityTaskCancel   = "task.cancel.v1"
)

var (
	ErrDiagnosisCapability = errors.New("node agent does not support diagnostics")
	ErrNodeNotRegistered   = errors.New("node has not registered")
	diagnosticSecret       = regexp.MustCompile(`(?i)("?(?:authorization|password|passwd|token|secret|api[_-]?key)"?\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;]+)`)
	diagnosticBearer       = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/-]+=*`)
	diagnosticURLSecret    = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://[^:/\s]+:)[^@\s]+@`)
	pingLossPattern        = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)%\s+packet loss`)
	pingAveragePattern     = regexp.MustCompile(`=\s*[0-9.]+/([0-9.]+)/`)
)

type DiagnosisPayload struct {
	Policy        models.ClusterPolicy `json:"policy"`
	ControllerURL string               `json:"controllerUrl,omitempty"`
}

type DiagnosisSummary struct {
	Passed      int `json:"passed"`
	Warning     int `json:"warning"`
	Failed      int `json:"failed"`
	Skipped     int `json:"skipped"`
	Unsupported int `json:"unsupported"`
}

type DiagnosisCheck struct {
	Key        string    `json:"key"`
	Status     string    `json:"status"`
	Value      any       `json:"value,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	Evidence   string    `json:"evidence,omitempty"`
	ObservedAt time.Time `json:"observedAt"`
}

type DiagnosisResult struct {
	OverallStatus string           `json:"overallStatus"`
	Summary       DiagnosisSummary `json:"summary"`
	Checks        []DiagnosisCheck `json:"checks"`
	StartedAt     time.Time        `json:"startedAt"`
	FinishedAt    time.Time        `json:"finishedAt"`
}

type diagnosticEndpoint struct {
	Kind    string
	Scheme  string
	Address string
}

type diagnosticEndpointObservation struct {
	Kind      string `json:"kind"`
	Target    string `json:"target"`
	Protocol  string `json:"protocol"`
	Reachable bool   `json:"reachable"`
	Failure   string `json:"failure,omitempty"`
}

type diagnosisEnqueueError struct {
	stage string
	err   error
}

func (e *diagnosisEnqueueError) Error() string {
	return fmt.Sprintf("%s: %v", e.stage, e.err)
}

func (e *diagnosisEnqueueError) Unwrap() error { return e.err }

// SafeErrorDetail exposes only an actionable category. Raw database errors can
// contain schema or storage details and are kept in the Panel runtime log.
func (e *diagnosisEnqueueError) SafeErrorDetail(_ error) string {
	lower := strings.ToLower(e.err.Error())
	switch {
	case strings.Contains(lower, "no such column"),
		strings.Contains(lower, "has no column named"),
		strings.Contains(lower, "no such table"):
		return "集群数据库结构尚未完成升级，请重启 Panel 服务完成数据库迁移后重试"
	case strings.Contains(lower, "database is locked"),
		strings.Contains(lower, "database is busy"):
		return "集群任务数据库当前繁忙，请稍后重试；若持续失败请查看 Panel 运行日志"
	default:
		return "诊断任务未能写入任务队列，请结合请求时间查看 Panel 运行日志"
	}
}

func (e *diagnosisEnqueueError) ErrorCode() string {
	return "CLUSTER_DIAGNOSIS_ENQUEUE_FAILED"
}

func wrapDiagnosisEnqueueError(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &diagnosisEnqueueError{stage: stage, err: err}
}

func (m *Manager) EnqueueDiagnosis(nodeID uint, batchID string, requestedBy int64) (models.ClusterTask, error) {
	node, err := m.GetNode(nodeID)
	if err != nil {
		return models.ClusterTask{}, wrapDiagnosisEnqueueError("load node", err)
	}
	if node.Status == models.ClusterNodeStatusPending || node.LastRegisteredAt == nil {
		return models.ClusterTask{}, ErrNodeNotRegistered
	}
	policy, err := m.GetPolicy()
	if err != nil {
		return models.ClusterTask{}, wrapDiagnosisEnqueueError("load policy", err)
	}
	payload, err := json.Marshal(DiagnosisPayload{Policy: policy, ControllerURL: node.Endpoint})
	if err != nil {
		return models.ClusterTask{}, wrapDiagnosisEnqueueError("encode payload", err)
	}
	cancelable := true
	task, err := m.EnqueueTask(EnqueueTaskInput{
		NodeID: nodeID, BatchID: batchID, Type: TaskNodeDiagnose, Payload: payload,
		IdempotencyKey: batchTaskIdempotencyKey(batchID, nodeID, TaskNodeDiagnose),
		MaxAttempts:    1, RequestedBy: requestedBy, Cancelable: &cancelable, internal: true,
	})
	if err != nil {
		return models.ClusterTask{}, wrapDiagnosisEnqueueError("enqueue task", err)
	}
	if !nodeHeartbeatFresh(node, time.Now()) {
		result := offlineDiagnosisResult(node)
		encoded, _ := json.Marshal(result)
		now := time.Now()
		updates := map[string]any{
			"status": models.ClusterTaskStatusFailed, "stage": models.ClusterTaskStatusFailed,
			"progress": 100, "result": string(encoded), "error": "node agent is offline",
			"finished_at": &now,
		}
		if updateErr := m.db.Model(&models.ClusterTask{}).Where("id = ?", task.ID).Updates(updates).Error; updateErr != nil {
			return models.ClusterTask{}, wrapDiagnosisEnqueueError("finish offline diagnosis", updateErr)
		}
		_ = m.appendTaskEvent(task.ID, "diagnosis_offline", models.ClusterTaskStatusFailed, "error", "agent_offline", 100, "Agent 离线，已生成终态诊断结果")
		_ = m.db.First(&task, task.ID).Error
		_ = m.updateBatchStatus(batchID)
		return task, nil
	}
	if !hasCapability(node.Capabilities, CapabilityNodeDiagnose) {
		now := time.Now()
		_ = m.db.Model(&models.ClusterTask{}).Where("id = ?", task.ID).Updates(map[string]any{
			"status": models.ClusterTaskStatusFailed, "stage": models.ClusterTaskStatusFailed,
			"progress": 100, "error": ErrDiagnosisCapability.Error(), "finished_at": &now,
		}).Error
		_ = m.appendTaskEvent(task.ID, "unsupported", models.ClusterTaskStatusFailed, "error", "agent_upgrade_required", 100, "Agent 版本不支持一键诊断，请先升级 Agent")
		_ = m.db.First(&task, task.ID).Error
		_ = m.updateBatchStatus(batchID)
		return task, ErrDiagnosisCapability
	}
	return task, nil
}

func hasCapability(capabilities []string, target string) bool {
	for _, capability := range capabilities {
		if strings.EqualFold(strings.TrimSpace(capability), target) {
			return true
		}
	}
	return false
}

func offlineDiagnosisResult(node models.ClusterNode) DiagnosisResult {
	now := time.Now().UTC()
	checks := []DiagnosisCheck{
		{Key: "agent", Status: "failed", Value: "offline", Detail: "Agent 未在线或心跳已过期", ObservedAt: now},
		{Key: "communication", Status: "failed", Value: node.Status, Detail: "控制端无法向节点下发诊断任务", ObservedAt: now},
	}
	for _, key := range []string{"cpu", "memory", "disk", "disk_inode", "dns", "network", "service_reachability", "ports", "firewall", "security_group", "time_sync", "services", "recent_logs"} {
		checks = append(checks, DiagnosisCheck{Key: key, Status: "skipped", Detail: "Agent 离线，节点侧检查已跳过", ObservedAt: now})
	}
	result := DiagnosisResult{OverallStatus: "critical", Checks: checks, StartedAt: now, FinishedAt: now}
	result.Summary = summarizeDiagnosis(checks)
	return result
}

func (a *Agent) executeDiagnosis(ctx context.Context, raw string) (json.RawMessage, error) {
	startedAt := time.Now().UTC()
	var payload DiagnosisPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, err
	}
	checks := make([]DiagnosisCheck, 0, 16)
	add := func(check DiagnosisCheck) {
		if check.ObservedAt.IsZero() {
			check.ObservedAt = time.Now().UTC()
		}
		check.Detail = boundedText(check.Detail, 1024)
		check.Evidence = boundedText(sanitizeDiagnosticText(check.Evidence), 32*1024)
		checks = append(checks, check)
	}
	add(DiagnosisCheck{Key: "agent", Status: "passed", Value: "online", Detail: "Agent 已领取并执行诊断任务"})
	add(DiagnosisCheck{Key: "communication", Status: "passed", Value: "connected", Detail: "Agent 与控制端任务通道正常"})

	snapshot, _, _, snapshotErr := collectHostSnapshot(ctx, monitoring.NewSystemCollector())
	if snapshotErr != nil {
		for _, key := range []string{"cpu", "memory", "disk"} {
			add(DiagnosisCheck{Key: key, Status: "failed", Detail: "无法读取主机指标"})
		}
	} else {
		add(metricDiagnosisCheck("cpu", snapshot.CPUPercent, payload.Policy.CPUWarningThreshold, payload.Policy.CPUCriticalThreshold))
		add(metricDiagnosisCheck("memory", snapshot.MemoryPercent, payload.Policy.MemoryWarningThreshold, payload.Policy.MemoryCriticalThreshold))
		add(metricDiagnosisCheck("disk", snapshot.DiskPercent, payload.Policy.DiskWarningThreshold, payload.Policy.DiskCriticalThreshold))
	}
	if usage, err := disk.UsageWithContext(ctx, "/"); err != nil {
		add(DiagnosisCheck{Key: "disk_inode", Status: "unsupported", Detail: "当前系统无法读取 inode 使用率"})
	} else {
		add(metricDiagnosisCheck("disk_inode", usage.InodesUsedPercent, payload.Policy.DiskWarningThreshold, payload.Policy.DiskCriticalThreshold))
	}

	controllerTargets, centerTargets := configuredDiagnosticTargets(a.cfg.ControllerURL)
	dnsTargets := uniqueStrings(append(append([]string{}, payload.Policy.DNSTargets...), append(controllerTargets, centerTargets...)...))
	add(checkDNS(ctx, dnsTargets))
	add(checkNetwork(ctx, uniqueStrings(payload.Policy.NetworkTargets)))
	add(checkDiagnosticEndpoints(ctx, configuredDiagnosticEndpoints(a.cfg.ControllerURL)))

	ports := append([]int{22}, payload.Policy.CommonPorts...)
	installedPorts, installedServices := installedDiagnosticAssets()
	ports = append(ports, installedPorts...)
	if value, err := strconv.Atoi(strings.TrimSpace(app.ONE_CONFIG.System.Port)); err == nil {
		ports = append(ports, value)
	}
	if app.ONE_CONFIG.System.HTTPSEnabled {
		if value, err := strconv.Atoi(strings.TrimSpace(app.ONE_CONFIG.System.HTTPSPort)); err == nil {
			ports = append(ports, value)
		}
	}
	add(checkPorts(ctx, uniquePorts(ports)))
	add(checkFirewall(ctx))
	add(DiagnosisCheck{Key: "security_group", Status: "unsupported", Detail: "云安全组无法在节点本机可靠判定"})
	add(checkTimeSync(ctx))
	services := append([]string{"one.service"}, payload.Policy.ExtraServices...)
	services = append(services, installedServices...)
	add(checkServices(ctx, uniqueStrings(services)))
	add(checkRecentLogs(ctx, uniqueStrings(services), payload.Policy.LogLookbackMinutes, payload.Policy.MaxLogEntries))

	result := DiagnosisResult{Checks: checks, StartedAt: startedAt, FinishedAt: time.Now().UTC()}
	result.Summary = summarizeDiagnosis(checks)
	result.OverallStatus = "healthy"
	if result.Summary.Failed > 0 {
		result.OverallStatus = "critical"
	} else if result.Summary.Warning > 0 {
		result.OverallStatus = "warning"
	}
	return json.Marshal(result)
}

func installedDiagnosticAssets() ([]int, []string) {
	var rows []models.Software
	if app.DB() == nil || app.DB().Where("installed = ?", true).Find(&rows).Error != nil {
		return nil, nil
	}
	ports := []int{}
	services := []string{}
	for _, row := range rows {
		for _, raw := range []string{row.HttpPort, row.HttpsPort} {
			if value, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
				ports = append(ports, value)
			}
		}
		service := strings.TrimSpace(row.ServiceName)
		if service == "" || !serviceUnitPattern.MatchString(service) {
			continue
		}
		if !strings.HasSuffix(service, ".service") {
			service += ".service"
		}
		services = append(services, service)
	}
	return uniquePorts(ports), uniqueStrings(services)
}

func metricDiagnosisCheck(key string, value, warning, critical float64) DiagnosisCheck {
	status := "passed"
	if value >= critical {
		status = "failed"
	} else if value >= warning {
		status = "warning"
	}
	return DiagnosisCheck{Key: key, Status: status, Value: value, Detail: fmt.Sprintf("当前使用率 %.1f%%", value)}
}

func configuredDiagnosticTargets(controllerURL string) ([]string, []string) {
	controller := hostnameFromURL(controllerURL)
	centers := []string{}
	if app.ONE_CONFIG.UpdateCenter.Enabled {
		if value := hostnameFromURL(app.ONE_CONFIG.UpdateCenter.CenterURL); value != "" {
			centers = append(centers, value)
		}
	}
	if app.ONE_CONFIG.ScriptCenter.Enabled {
		if value := hostnameFromURL(app.ONE_CONFIG.ScriptCenter.URL); value != "" {
			centers = append(centers, value)
		}
	}
	controllers := []string{}
	if controller != "" {
		controllers = append(controllers, controller)
	}
	return uniqueStrings(controllers), uniqueStrings(centers)
}

func configuredDiagnosticEndpoints(controllerURL string) []diagnosticEndpoint {
	endpoints := make([]diagnosticEndpoint, 0, 3)
	seen := map[string]struct{}{}
	add := func(kind, rawURL string) {
		parsed, err := url.Parse(strings.TrimSpace(rawURL))
		if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return
		}
		port := parsed.Port()
		if port == "" {
			if parsed.Scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		address := net.JoinHostPort(parsed.Hostname(), port)
		if _, ok := seen[address]; ok {
			return
		}
		seen[address] = struct{}{}
		endpoints = append(endpoints, diagnosticEndpoint{Kind: kind, Scheme: parsed.Scheme, Address: address})
	}
	add("controller", controllerURL)
	if app.ONE_CONFIG.UpdateCenter.Enabled {
		add("center", app.ONE_CONFIG.UpdateCenter.CenterURL)
	}
	if app.ONE_CONFIG.ScriptCenter.Enabled {
		add("center", app.ONE_CONFIG.ScriptCenter.URL)
	}
	return endpoints
}

func checkDiagnosticEndpoints(ctx context.Context, endpoints []diagnosticEndpoint) DiagnosisCheck {
	if len(endpoints) == 0 {
		return DiagnosisCheck{Key: "service_reachability", Status: "skipped", Detail: "未配置已启用的 Controller 或 Center 地址"}
	}
	observations := make([]diagnosticEndpointObservation, 0, len(endpoints))
	failed := make([]string, 0)
	for _, endpoint := range endpoints {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		connection, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(probeCtx, "tcp", endpoint.Address)
		cancel()
		observation := diagnosticEndpointObservation{Kind: endpoint.Kind, Target: endpoint.Address, Protocol: endpoint.Scheme, Reachable: err == nil}
		if err != nil {
			observation.Failure = "TCP 端口不可达"
			failed = append(failed, endpoint.Address)
		} else {
			_ = connection.Close()
		}
		observations = append(observations, observation)
	}
	if len(failed) > 0 {
		return DiagnosisCheck{Key: "service_reachability", Status: "failed", Value: observations, Detail: "部分 Controller 或 Center TCP 端口不可达"}
	}
	return DiagnosisCheck{Key: "service_reachability", Status: "passed", Value: observations, Detail: "Controller 和 Center TCP 端口均可达"}
}

func hostnameFromURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func checkDNS(ctx context.Context, targets []string) DiagnosisCheck {
	domains := make([]string, 0, len(targets))
	for _, target := range targets {
		if net.ParseIP(target) == nil {
			domains = append(domains, target)
		}
	}
	if len(domains) == 0 {
		return DiagnosisCheck{Key: "dns", Status: "skipped", Detail: "未配置可解析的 Controller、Center 或额外域名"}
	}
	failed := []string{}
	for _, target := range domains {
		lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := net.DefaultResolver.LookupHost(lookupCtx, target)
		cancel()
		if err != nil {
			failed = append(failed, target)
		}
	}
	if len(failed) > 0 {
		return DiagnosisCheck{Key: "dns", Status: "failed", Value: failed, Detail: "部分域名解析失败"}
	}
	return DiagnosisCheck{Key: "dns", Status: "passed", Value: domains, Detail: "DNS 解析正常"}
}

func checkNetwork(ctx context.Context, targets []string) DiagnosisCheck {
	if len(targets) == 0 {
		return DiagnosisCheck{Key: "network", Status: "skipped", Detail: "未配置网络探测目标且未发现默认网关"}
	}
	if _, err := exec.LookPath("ping"); err != nil {
		return DiagnosisCheck{Key: "network", Status: "unsupported", Detail: "系统未安装 ping"}
	}
	type sample struct {
		Target  string  `json:"target"`
		Latency float64 `json:"latencyMs,omitempty"`
		Loss    float64 `json:"lossPercent"`
	}
	samples := make([]sample, 0, len(targets))
	status := "passed"
	for _, target := range targets {
		probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		output, err := exec.CommandContext(probeCtx, "ping", "-c", "4", "-W", "2", target).CombinedOutput()
		cancel()
		text := string(output)
		item := sample{Target: target, Loss: 100}
		if match := pingLossPattern.FindStringSubmatch(text); len(match) == 2 {
			item.Loss, _ = strconv.ParseFloat(match[1], 64)
		}
		if match := pingAveragePattern.FindStringSubmatch(text); len(match) == 2 {
			item.Latency, _ = strconv.ParseFloat(match[1], 64)
		}
		if err != nil || item.Loss >= 50 {
			status = "failed"
		} else if item.Loss > 0 || item.Latency >= 200 {
			if status != "failed" {
				status = "warning"
			}
		}
		samples = append(samples, item)
	}
	return DiagnosisCheck{Key: "network", Status: status, Value: samples, Detail: "固定 4 次 ICMP 探测结果"}
}

func checkPorts(ctx context.Context, ports []int) DiagnosisCheck {
	if len(ports) == 0 {
		return DiagnosisCheck{Key: "ports", Status: "skipped", Detail: "未配置受管端口"}
	}
	closed := []int{}
	for _, port := range ports {
		dialer := net.Dialer{Timeout: 800 * time.Millisecond}
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			closed = append(closed, port)
			continue
		}
		_ = conn.Close()
	}
	if len(closed) > 0 {
		return DiagnosisCheck{Key: "ports", Status: "warning", Value: map[string]any{"checked": ports, "notListening": closed}, Detail: "部分受管端口未监听"}
	}
	return DiagnosisCheck{Key: "ports", Status: "passed", Value: ports, Detail: "受管端口均在本机监听"}
}

func checkFirewall(ctx context.Context) DiagnosisCheck {
	if _, err := exec.LookPath("ufw"); err == nil {
		output, commandErr := fixedCommand(ctx, 5*time.Second, "ufw", "status")
		status := "passed"
		if commandErr != nil || strings.Contains(strings.ToLower(output), "status: inactive") {
			status = "warning"
		}
		return DiagnosisCheck{Key: "firewall", Status: status, Value: "ufw", Detail: "已读取 UFW 状态", Evidence: output}
	}
	if _, err := exec.LookPath("firewall-cmd"); err == nil {
		output, commandErr := fixedCommand(ctx, 5*time.Second, "firewall-cmd", "--state")
		status := "passed"
		if commandErr != nil {
			status = "warning"
		}
		return DiagnosisCheck{Key: "firewall", Status: status, Value: "firewalld", Detail: "已读取 firewalld 状态", Evidence: output}
	}
	return DiagnosisCheck{Key: "firewall", Status: "unsupported", Detail: "未发现 UFW 或 firewalld"}
}

func checkTimeSync(ctx context.Context) DiagnosisCheck {
	if _, err := exec.LookPath("timedatectl"); err == nil {
		output, commandErr := fixedCommand(ctx, 5*time.Second, "timedatectl", "show", "-p", "NTPSynchronized", "--value")
		value := strings.ToLower(strings.TrimSpace(output))
		if commandErr == nil && value == "yes" {
			return DiagnosisCheck{Key: "time_sync", Status: "passed", Value: true, Detail: "系统时间已同步"}
		}
	}
	if _, err := exec.LookPath("chronyc"); err == nil {
		output, commandErr := fixedCommand(ctx, 5*time.Second, "chronyc", "tracking")
		lower := strings.ToLower(output)
		if commandErr == nil && strings.Contains(lower, "leap status") && !strings.Contains(lower, "not synchronised") && !strings.Contains(lower, "unsynchronised") {
			return DiagnosisCheck{Key: "time_sync", Status: "passed", Value: "chrony", Detail: "chrony 时间同步正常", Evidence: output}
		}
		return DiagnosisCheck{Key: "time_sync", Status: "warning", Value: "chrony", Detail: "chrony 未确认同步", Evidence: output}
	}
	if _, err := exec.LookPath("systemctl"); err == nil {
		output, commandErr := fixedCommand(ctx, 5*time.Second, "systemctl", "is-active", "systemd-timesyncd.service")
		if commandErr == nil && strings.TrimSpace(output) == "active" {
			return DiagnosisCheck{Key: "time_sync", Status: "passed", Value: "systemd-timesyncd", Detail: "systemd-timesyncd 正在运行"}
		}
	}
	return DiagnosisCheck{Key: "time_sync", Status: "warning", Detail: "未能确认系统时间同步状态"}
}

func checkServices(ctx context.Context, configured []string) DiagnosisCheck {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return DiagnosisCheck{Key: "services", Status: "unsupported", Detail: "当前系统不支持 systemctl 状态探测"}
	}
	services := append([]string{}, configured...)
	sshService := "sshd.service"
	if output, err := fixedCommand(ctx, 4*time.Second, "systemctl", "is-active", "ssh.service"); err == nil && strings.TrimSpace(output) == "active" {
		sshService = "ssh.service"
	}
	services = uniqueStrings(append(services, sshService))
	failed := []string{}
	for _, service := range services {
		output, err := fixedCommand(ctx, 5*time.Second, "systemctl", "is-active", service)
		if err != nil || strings.TrimSpace(output) != "active" {
			failed = append(failed, service)
		}
	}
	if len(failed) > 0 {
		return DiagnosisCheck{Key: "services", Status: "failed", Value: map[string]any{"checked": services, "inactive": failed}, Detail: "部分核心或受管服务未运行"}
	}
	return DiagnosisCheck{Key: "services", Status: "passed", Value: services, Detail: "核心和受管服务运行正常"}
}

func checkRecentLogs(ctx context.Context, services []string, lookbackMinutes, maximum int) DiagnosisCheck {
	if _, err := exec.LookPath("journalctl"); err != nil {
		return DiagnosisCheck{Key: "recent_logs", Status: "unsupported", Detail: "系统未提供 journalctl"}
	}
	if lookbackMinutes < 1 || lookbackMinutes > 1440 {
		lookbackMinutes = 30
	}
	if maximum < 10 || maximum > 1000 {
		maximum = 200
	}
	args := []string{"--since", fmt.Sprintf("-%dm", lookbackMinutes), "-p", "err..alert", "--no-pager", "-n", strconv.Itoa(maximum), "-o", "short-iso"}
	for _, service := range services {
		args = append(args, "-u", service)
	}
	output, err := fixedCommand(ctx, 10*time.Second, "journalctl", args...)
	if err != nil {
		return DiagnosisCheck{Key: "recent_logs", Status: "warning", Detail: "无法完整读取关键错误日志", Evidence: output}
	}
	lines := nonEmptyLines(output)
	if len(lines) == 0 || strings.Contains(strings.ToLower(output), "-- no entries --") {
		return DiagnosisCheck{Key: "recent_logs", Status: "passed", Value: 0, Detail: "日志窗口内未发现关键错误"}
	}
	return DiagnosisCheck{Key: "recent_logs", Status: "warning", Value: len(lines), Detail: "日志窗口内发现关键错误", Evidence: output}
}

func fixedCommand(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := exec.CommandContext(commandCtx, name, args...).CombinedOutput()
	text := string(output)
	if len(text) > 64*1024 {
		text = text[:64*1024]
	}
	return text, err
}

func summarizeDiagnosis(checks []DiagnosisCheck) DiagnosisSummary {
	var result DiagnosisSummary
	for _, check := range checks {
		switch check.Status {
		case "passed":
			result.Passed++
		case "warning":
			result.Warning++
		case "failed":
			result.Failed++
		case "skipped":
			result.Skipped++
		case "unsupported":
			result.Unsupported++
		}
	}
	return result
}

func sanitizeDiagnosisResult(raw json.RawMessage) (json.RawMessage, error) {
	var result DiagnosisResult
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Checks) == 0 || len(result.Checks) > 64 {
		return nil, errors.New("diagnosis result is invalid")
	}
	for i := range result.Checks {
		check := &result.Checks[i]
		if strings.TrimSpace(check.Key) == "" || len(check.Key) > 64 {
			return nil, errors.New("diagnosis check key is invalid")
		}
		switch check.Status {
		case "passed", "warning", "failed", "skipped", "unsupported":
		default:
			return nil, errors.New("diagnosis check status is invalid")
		}
		check.Detail = boundedText(sanitizeDiagnosticText(check.Detail), 1024)
		check.Evidence = boundedText(sanitizeDiagnosticText(check.Evidence), 32*1024)
		if check.ObservedAt.IsZero() {
			check.ObservedAt = time.Now().UTC()
		}
	}
	result.Summary = summarizeDiagnosis(result.Checks)
	result.OverallStatus = "healthy"
	if result.Summary.Failed > 0 {
		result.OverallStatus = "critical"
	} else if result.Summary.Warning > 0 {
		result.OverallStatus = "warning"
	}
	if result.StartedAt.IsZero() {
		result.StartedAt = time.Now().UTC()
	}
	if result.FinishedAt.IsZero() {
		result.FinishedAt = time.Now().UTC()
	}
	return json.Marshal(result)
}

func sanitizeDiagnosticText(value string) string {
	value = diagnosticSecret.ReplaceAllString(value, "$1[REDACTED]")
	value = diagnosticBearer.ReplaceAllString(value, "$1[REDACTED]")
	value = diagnosticURLSecret.ReplaceAllString(value, "$1[REDACTED]@")
	return strings.TrimSpace(value)
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func uniquePorts(values []int) []int {
	seen := map[int]struct{}{}
	result := []int{}
	for _, value := range values {
		if value < 1 || value > 65535 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Ints(result)
	return result
}

func nonEmptyLines(value string) []string {
	result := []string{}
	for _, line := range strings.Split(value, "\n") {
		if strings.TrimSpace(line) != "" && !strings.Contains(strings.ToLower(line), "-- no entries --") {
			result = append(result, line)
		}
	}
	return result
}

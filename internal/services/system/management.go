package system

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/process"
)

type ProcessSummary struct {
	PID        int32   `json:"pid"`
	PPID       int32   `json:"ppid"`
	Name       string  `json:"name"`
	Username   string  `json:"username,omitempty"`
	Status     string  `json:"status,omitempty"`
	CPUPercent float64 `json:"cpuPercent"`
	MemoryRSS  uint64  `json:"memoryRss"`
	CreateTime int64   `json:"createTime,omitempty"`
}

type ProcessDetail struct {
	ProcessSummary
	Executable string  `json:"executable,omitempty"`
	Command    string  `json:"command,omitempty"`
	CWD        string  `json:"cwd,omitempty"`
	Children   []int32 `json:"children,omitempty"`
}

type ProcessList struct {
	Items  []ProcessSummary `json:"items"`
	Total  int              `json:"total"`
	Offset int              `json:"offset"`
	Limit  int              `json:"limit"`
}

var ErrProcessNotAvailable = errors.New("process is no longer available")

func ListProcesses(ctx context.Context, offset, limit int, keyword, sortBy string, descending bool) (ProcessList, error) {
	if offset < 0 || limit < 1 || limit > 200 {
		return ProcessList{}, errors.New("invalid process pagination")
	}
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	items := make([]ProcessSummary, 0)
	processes, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return ProcessList{}, fmt.Errorf("list processes: %w", err)
	}
	for _, p := range processes {
		name, _ := p.NameWithContext(ctx)
		username, _ := p.UsernameWithContext(ctx)
		statusValues, _ := p.StatusWithContext(ctx)
		status := ""
		if len(statusValues) > 0 {
			status = statusValues[0]
		}
		if keyword != "" && !strings.Contains(strings.ToLower(name), keyword) && !strings.Contains(strconv.Itoa(int(p.Pid)), keyword) {
			continue
		}
		memory, _ := p.MemoryInfoWithContext(ctx)
		var rss uint64
		if memory != nil {
			rss = memory.RSS
		}
		cpuPercent, _ := p.CPUPercentWithContext(ctx)
		createTime, _ := p.CreateTimeWithContext(ctx)
		ppid, _ := p.PpidWithContext(ctx)
		items = append(items, ProcessSummary{PID: p.Pid, PPID: ppid, Name: name, Username: username, Status: status, CPUPercent: cpuPercent, MemoryRSS: rss, CreateTime: createTime})
	}
	sort.SliceStable(items, func(i, j int) bool {
		var less bool
		switch sortBy {
		case "cpu":
			less = items[i].CPUPercent < items[j].CPUPercent
		case "memory":
			less = items[i].MemoryRSS < items[j].MemoryRSS
		case "name":
			less = strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
		default:
			less = items[i].PID < items[j].PID
		}
		if descending {
			return !less
		}
		return less
	})
	total := len(items)
	if offset >= total {
		items = []ProcessSummary{}
	} else {
		end := offset + limit
		if end > total {
			end = total
		}
		items = items[offset:end]
	}
	return ProcessList{Items: items, Total: total, Offset: offset, Limit: limit}, nil
}

func GetProcessDetail(ctx context.Context, pid int32) (ProcessDetail, error) {
	if pid < 1 {
		return ProcessDetail{}, errors.New("invalid process id")
	}
	p, err := process.NewProcess(pid)
	if err != nil {
		return ProcessDetail{}, fmt.Errorf("%w: %v", ErrProcessNotAvailable, err)
	}
	list, err := ListProcesses(ctx, 0, 200, strconv.Itoa(int(pid)), "pid", false)
	if err != nil || len(list.Items) != 1 {
		return ProcessDetail{}, ErrProcessNotAvailable
	}
	detail := ProcessDetail{ProcessSummary: list.Items[0]}
	detail.Executable, _ = p.ExeWithContext(ctx)
	args, _ := p.CmdlineSliceWithContext(ctx)
	detail.Command = sanitizeProcessCommand(args)
	detail.CWD, _ = p.CwdWithContext(ctx)
	all, _ := process.ProcessesWithContext(ctx)
	for _, child := range all {
		parent, _ := child.PpidWithContext(ctx)
		if parent == pid {
			detail.Children = append(detail.Children, child.Pid)
		}
	}
	sort.Slice(detail.Children, func(i, j int) bool { return detail.Children[i] < detail.Children[j] })
	return detail, nil
}

var processInlineSensitiveAssignment = regexp.MustCompile(`(?i)(^|[^[:alnum:]_-])((?:[[:alnum:]]+[-_])*(?:password|passwd|passphrase|pwd|token|secret|key|apikey|accesstoken|refreshtoken|authtoken|clientsecret|clientkey|privatekey|publickey|secretkey|encryptionkey|signingkey)(?:[-_][[:alnum:]]+)*)(=|:)(?:"[^"]*"|'[^']*'|[^[:space:]&;,}]+)`)

func sanitizeProcessCommand(args []string) string {
	sanitized := make([]string, len(args))
	redactNext := false
	for i, arg := range args {
		if redactNext {
			sanitized[i] = "[REDACTED]"
			redactNext = false
			continue
		}

		valueStart, hasValue, sensitive := processSensitiveArgument(arg)
		if !sensitive {
			sanitized[i] = redactInlineProcessArgument(arg)
			continue
		}
		if hasValue {
			sanitized[i] = arg[:valueStart] + "[REDACTED]"
			continue
		}

		sanitized[i] = arg
		if i+1 < len(args) {
			redactNext = true
		} else {
			sanitized[i] += "=[REDACTED]"
		}
	}
	return strings.Join(sanitized, " ")
}

func processSensitiveArgument(arg string) (valueStart int, hasValue, sensitive bool) {
	if arg == "-p" {
		return 0, false, true
	}
	if strings.HasPrefix(arg, "-p") && !strings.HasPrefix(arg, "--") && len(arg) > 2 {
		return 2, true, true
	}

	separator := strings.IndexAny(arg, "=:")
	if separator > 0 && isSensitiveProcessArgumentName(arg[:separator]) {
		return separator + 1, true, true
	}
	if strings.HasPrefix(arg, "--") && isSensitiveProcessArgumentName(arg[2:]) {
		return 0, false, true
	}
	return 0, false, false
}

func isSensitiveProcessArgumentName(name string) bool {
	name = strings.ToLower(strings.TrimLeft(strings.TrimSpace(name), "-"))
	name = strings.ReplaceAll(name, "_", "-")
	if name == "" {
		return false
	}

	for _, word := range []string{"password", "passwd", "passphrase", "pwd", "token", "secret"} {
		if strings.HasSuffix(name, word) || strings.HasPrefix(name, word+"-") || strings.Contains(name, "-"+word+"-") {
			return true
		}
	}
	for _, compactName := range []string{"apikey", "accesstoken", "refreshtoken", "authtoken", "clientsecret", "clientkey", "privatekey", "publickey", "secretkey", "encryptionkey", "signingkey"} {
		if name == compactName {
			return true
		}
	}
	return strings.HasSuffix(name, "key") || strings.HasPrefix(name, "key-") || strings.Contains(name, "-key-")
}

func redactInlineProcessArgument(arg string) string {
	matches := processInlineSensitiveAssignment.FindAllStringSubmatchIndex(arg, -1)
	if len(matches) == 0 {
		return arg
	}

	var builder strings.Builder
	cursor := 0
	for _, match := range matches {
		if len(match) < 8 || match[0] < cursor {
			continue
		}
		valueStart := match[7]
		valueEnd := match[1]
		if valueStart > len(arg) || valueEnd < valueStart {
			continue
		}
		builder.WriteString(arg[cursor:valueStart])
		builder.WriteString("[REDACTED]")
		cursor = valueEnd
	}
	if cursor == 0 {
		return arg
	}
	builder.WriteString(arg[cursor:])
	return builder.String()
}

type DiskDevice struct {
	Device     string `json:"device"`
	Mountpoint string `json:"mountpoint,omitempty"`
	FSType     string `json:"fsType,omitempty"`
	Options    string `json:"options,omitempty"`
	TotalBytes uint64 `json:"totalBytes,omitempty"`
	UsedBytes  uint64 `json:"usedBytes,omitempty"`
	FreeBytes  uint64 `json:"freeBytes,omitempty"`
	UUID       string `json:"uuid,omitempty"`
	Persistent bool   `json:"persistent"`
}

type DiskInventory struct {
	Devices  []DiskDevice `json:"devices"`
	FSTab    []string     `json:"fstab"`
	Total    int          `json:"total"`
	Page     int          `json:"page"`
	PageSize int          `json:"pageSize"`
}

func GetDiskInventory(ctx context.Context, page, pageSize int) (DiskInventory, error) {
	if page < 1 || pageSize < 1 || pageSize > 100 {
		return DiskInventory{}, errors.New("invalid disk pagination")
	}
	partitions, err := disk.PartitionsWithContext(ctx, true)
	if err != nil {
		return DiskInventory{}, fmt.Errorf("list disk partitions: %w", err)
	}
	persistent := readFSTabDevices()
	devices := make([]DiskDevice, 0, len(partitions))
	for _, partition := range partitions {
		item := DiskDevice{Device: partition.Device, Mountpoint: partition.Mountpoint, FSType: partition.Fstype, Options: strings.Join(partition.Opts, ",")}
		usage, usageErr := disk.UsageWithContext(ctx, partition.Mountpoint)
		if usageErr == nil && usage != nil {
			item.TotalBytes, item.UsedBytes, item.FreeBytes = usage.Total, usage.Used, usage.Free
		}
		item.Persistent = persistent[partition.Device] || persistent[partition.Mountpoint]
		devices = append(devices, item)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Mountpoint < devices[j].Mountpoint })
	total := len(devices)
	if page-1 > total/pageSize {
		devices = []DiskDevice{}
	} else {
		start := (page - 1) * pageSize
		end := start + pageSize
		if end > total {
			end = total
		}
		devices = devices[start:end]
	}
	return DiskInventory{Devices: devices, FSTab: readFSTabLines(), Total: total, Page: page, PageSize: pageSize}, nil
}

func readFSTabLines() []string {
	file, err := os.Open("/etc/fstab")
	if err != nil {
		return []string{}
	}
	defer file.Close()
	lines := make([]string, 0)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	return lines
}

func readFSTabDevices() map[string]bool {
	result := make(map[string]bool)
	for _, line := range readFSTabLines() {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			result[fields[0]] = true
			result[fields[1]] = true
		}
	}
	return result
}

type SSHConfig struct {
	Supported              bool   `json:"supported"`
	Service                string `json:"service,omitempty"`
	ConfigPath             string `json:"configPath,omitempty"`
	Port                   string `json:"port,omitempty"`
	PasswordAuthentication string `json:"passwordAuthentication,omitempty"`
	PermitRootLogin        string `json:"permitRootLogin,omitempty"`
	PubkeyAuthentication   string `json:"pubkeyAuthentication,omitempty"`
	PermitEmptyPasswords   string `json:"permitEmptyPasswords,omitempty"`
	ListenAddress          string `json:"listenAddress,omitempty"`
	Error                  string `json:"error,omitempty"`
}

const (
	maxSSHCommandOutputBytes = 64 << 10
	maxSSHDiagnosticRunes    = 512
)

func GetSSHConfig(ctx context.Context) SSHConfig {
	path := "/etc/ssh/sshd_config"
	config := SSHConfig{Supported: true, ConfigPath: filepath.Clean(path)}
	info, err := os.Stat(path)
	if err != nil {
		config.Supported = false
		config.Error = sshConfigPathError(path, err)
		return config
	}
	if !info.Mode().IsRegular() {
		config.Supported = false
		config.Error = fmt.Sprintf("sshd 配置路径不是普通文件：%s", path)
		return config
	}

	diagnostics := make([]string, 0, 2)
	sshdPath, err := exec.LookPath("sshd")
	if err != nil {
		diagnostics = append(diagnostics, "无法执行 sshd -T：sshd 命令不存在或不在 PATH 中")
	} else {
		var stdout, stderr sshCommandOutput
		stdout.limit = maxSSHCommandOutputBytes
		stderr.limit = maxSSHCommandOutputBytes
		command := exec.CommandContext(ctx, sshdPath, "-T", "-f", path)
		command.Stdout = &stdout
		command.Stderr = &stderr
		if runErr := command.Run(); runErr != nil {
			diagnostics = append(diagnostics, formatSSHCommandError(ctx, "sshd -T", runErr, &stderr, &stdout))
		} else if strings.TrimSpace(stdout.String()) == "" {
			diagnostics = append(diagnostics, "sshd -T 未返回生效配置")
		} else {
			parseSSHEffectiveConfig(&config, stdout.String())
			if !hasSSHEffectiveConfig(&config) {
				diagnostics = append(diagnostics, "sshd -T 返回的生效配置无法解析")
			}
		}
	}

	service, serviceDiagnostic := detectSSHService(ctx)
	config.Service = service
	if serviceDiagnostic != "" {
		diagnostics = append(diagnostics, serviceDiagnostic)
	}
	config.Error = strings.Join(diagnostics, "；")
	return config
}

func sshConfigPathError(path string, err error) string {
	switch {
	case os.IsNotExist(err):
		return fmt.Sprintf("sshd 配置文件不存在：%s", path)
	case os.IsPermission(err):
		return fmt.Sprintf("无权限读取 sshd 配置文件：%s", path)
	default:
		return fmt.Sprintf("无法访问 sshd 配置文件 %s：%s", path, sanitizeSSHDiagnostic(err.Error()))
	}
}

func formatSSHCommandError(ctx context.Context, commandName string, runErr error, stderr, stdout *sshCommandOutput) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Sprintf("%s 执行超时", commandName)
	}
	if ctx.Err() != nil {
		return fmt.Sprintf("%s 执行被取消：%s", commandName, sanitizeSSHDiagnostic(ctx.Err().Error()))
	}
	detail := sanitizeSSHDiagnostic(stderr.String())
	if detail == "" {
		detail = sanitizeSSHDiagnostic(stdout.String())
	}
	if detail == "" {
		detail = sanitizeSSHDiagnostic(runErr.Error())
	}
	return fmt.Sprintf("%s 执行失败：%s", commandName, detail)
}

func detectSSHService(ctx context.Context) (string, string) {
	systemctlPath, err := exec.LookPath("systemctl")
	if err != nil {
		return "", "无法检测 SSH 服务：systemctl 命令不存在或不在 PATH 中"
	}

	type serviceStatus struct {
		name   string
		status string
	}
	statuses := make([]serviceStatus, 0, 2)
	for _, service := range []string{"sshd", "ssh"} {
		var output sshCommandOutput
		output.limit = maxSSHCommandOutputBytes
		command := exec.CommandContext(ctx, systemctlPath, "is-active", "--no-pager", service+".service")
		command.Stdout = &output
		command.Stderr = &output
		runErr := command.Run()
		status := sanitizeSSHDiagnostic(output.String())
		if runErr == nil && strings.EqualFold(strings.TrimSpace(status), "active") {
			return service, ""
		}
		if status == "" {
			if runErr != nil {
				status = sanitizeSSHDiagnostic(runErr.Error())
			} else {
				status = "未知状态"
			}
		}
		statuses = append(statuses, serviceStatus{name: service + ".service", status: status})
	}

	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", "检测 SSH 服务状态超时"
		}
		return "", "检测 SSH 服务状态被取消"
	}

	details := make([]string, 0, len(statuses))
	allKnownStates := true
	for _, item := range statuses {
		details = append(details, item.name+"="+item.status)
		state := strings.ToLower(strings.TrimSpace(item.status))
		switch state {
		case "inactive", "failed", "unknown", "activating", "deactivating", "maintenance":
		default:
			allKnownStates = false
		}
	}
	if allKnownStates {
		return "", fmt.Sprintf("未检测到运行中的 SSH 服务：已检查 sshd.service、ssh.service（%s）；可能是服务未运行或服务名不匹配", strings.Join(details, "；"))
	}
	return "", fmt.Sprintf("SSH 服务状态检测失败：%s", strings.Join(details, "；"))
}

type sshCommandOutput struct {
	value     strings.Builder
	limit     int
	truncated bool
}

func (output *sshCommandOutput) Write(data []byte) (int, error) {
	remaining := output.limit - output.value.Len()
	if remaining > 0 {
		if len(data) < remaining {
			remaining = len(data)
		}
		_, _ = output.value.Write(data[:remaining])
	}
	if remaining < len(data) {
		output.truncated = true
	}
	return len(data), nil
}

func (output *sshCommandOutput) String() string {
	value := output.value.String()
	if output.truncated {
		value += " [输出已截断]"
	}
	return value
}

func sanitizeSSHDiagnostic(value string) string {
	value = strings.Map(func(char rune) rune {
		switch char {
		case '\n', '\r', '\t':
			return ' '
		case 0, 127:
			return -1
		default:
			if char < 32 {
				return -1
			}
			return char
		}
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if len([]rune(value)) > maxSSHDiagnosticRunes {
		value = string([]rune(value)[:maxSSHDiagnosticRunes]) + "…"
	}
	return value
}

func hasSSHEffectiveConfig(config *SSHConfig) bool {
	return config.Port != "" ||
		config.PasswordAuthentication != "" ||
		config.PermitRootLogin != "" ||
		config.PubkeyAuthentication != "" ||
		config.PermitEmptyPasswords != "" ||
		config.ListenAddress != ""
}

func parseSSHEffectiveConfig(config *SSHConfig, output string) {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		switch strings.ToLower(fields[0]) {
		case "port":
			config.Port = fields[1]
		case "passwordauthentication":
			config.PasswordAuthentication = fields[1]
		case "permitrootlogin":
			config.PermitRootLogin = fields[1]
		case "pubkeyauthentication":
			config.PubkeyAuthentication = fields[1]
		case "permitemptypasswords":
			config.PermitEmptyPasswords = fields[1]
		case "listenaddress":
			if config.ListenAddress == "" {
				config.ListenAddress = fields[1]
			}
		}
	}
}

package system

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	detail.Command = sanitizeProcessCommand(strings.Join(args, " "))
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

func sanitizeProcessCommand(value string) string {
	for _, marker := range []string{"password=", "token=", "secret=", "api_key="} {
		for {
			lower := strings.ToLower(value)
			start := strings.Index(lower, marker)
			if start < 0 {
				break
			}
			end := strings.IndexAny(value[start+len(marker):], " \t\r\n")
			if end < 0 {
				value = value[:start] + marker + "[REDACTED]"
			} else {
				value = value[:start] + marker + "[REDACTED]" + value[start+len(marker)+end:]
			}
		}
	}
	return value
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
	Devices []DiskDevice `json:"devices"`
	FSTab   []string     `json:"fstab"`
}

func GetDiskInventory(ctx context.Context) (DiskInventory, error) {
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
	return DiskInventory{Devices: devices, FSTab: readFSTabLines()}, nil
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

func GetSSHConfig(ctx context.Context) SSHConfig {
	path := "/etc/ssh/sshd_config"
	if _, err := os.Stat(path); err != nil {
		return SSHConfig{Error: "sshd 配置文件不存在"}
	}
	config := SSHConfig{Supported: true, ConfigPath: filepath.Clean(path)}
	if _, err := exec.LookPath("sshd"); err == nil {
		command := exec.CommandContext(ctx, "sshd", "-T", "-f", path)
		if output, runErr := command.Output(); runErr == nil {
			parseSSHEffectiveConfig(&config, string(output))
		} else {
			config.Error = "无法读取 sshd 生效配置"
		}
	} else {
		config.Error = "sshd 命令不可用"
	}
	for _, service := range []string{"sshd", "ssh"} {
		if _, err := exec.LookPath("systemctl"); err != nil {
			break
		}
		if exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", service).Run() == nil {
			config.Service = service
			break
		}
	}
	return config
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

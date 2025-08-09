package utils

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// CommandWhitelist 允许执行的命令白名单
var CommandWhitelist = map[string]bool{
	"systemctl": true,
	"ps":        true,
	"grep":      true,
	"nginx":     true,
	"ufw":       true,
	"iptables":  true,
	"mysql":     true,
	"redis-cli": true,
	"php":       true,
	"service":   true,
	"netstat":   true,
	"ss":        true,
	"lsof":      true,
	"which":     true,
	"whereis":   true,
	"id":        true,
	"whoami":    true,
	"pwd":       true,
	"ls":        true,
	"cat":       true,
	"tail":      true,
	"head":      true,
	"wc":        true,
}

// SafeCommand 安全的命令执行结构
type SafeCommand struct {
	Command string
	Args    []string
	Timeout time.Duration
}

// NewSafeCommand 创建安全命令
func NewSafeCommand(command string, args ...string) *SafeCommand {
	return &SafeCommand{
		Command: command,
		Args:    args,
		Timeout: 30 * time.Second, // 默认30秒超时
	}
}

// SetTimeout 设置超时时间
func (sc *SafeCommand) SetTimeout(timeout time.Duration) *SafeCommand {
	sc.Timeout = timeout
	return sc
}

// Validate 验证命令是否安全
func (sc *SafeCommand) Validate() error {
	// 检查命令是否在白名单中
	if !CommandWhitelist[sc.Command] {
		return fmt.Errorf("command '%s' is not allowed", sc.Command)
	}

	// 检查参数是否包含危险字符
	dangerousPatterns := []string{
		`;`,      // 命令分隔符
		`&&`,     // 逻辑与
		`||`,     // 逻辑或
		`|`,      // 管道
		`$`,      // 变量替换
		"`",      // 命令替换
		`>`,      // 重定向
		`<`,      // 重定向
		`rm`,     // 删除命令
		`dd`,     // 磁盘操作
		`mkfs`,   // 格式化
		`fdisk`,  // 磁盘分区
		`wget`,   // 下载
		`curl`,   // 下载
		`nc`,     // netcat
		`telnet`, // telnet
		`ssh`,    // ssh
		`scp`,    // scp
		`rsync`,  // rsync
	}

	for _, arg := range sc.Args {
		for _, pattern := range dangerousPatterns {
			if strings.Contains(arg, pattern) {
				return fmt.Errorf("argument contains dangerous pattern: %s", pattern)
			}
		}

		// 检查是否包含路径遍历
		if strings.Contains(arg, "../") || strings.Contains(arg, "..\\") {
			return fmt.Errorf("argument contains path traversal pattern")
		}
	}

	return nil
}

// Execute 安全执行命令
func (sc *SafeCommand) Execute() ([]byte, error) {
	if err := sc.Validate(); err != nil {
		return nil, err
	}

	cmd := exec.Command(sc.Command, sc.Args...)

	// 设置超时
	done := make(chan error, 1)
	var output []byte
	var execErr error

	go func() {
		output, execErr = cmd.Output()
		done <- execErr
	}()

	select {
	case err := <-done:
		return output, err
	case <-time.After(sc.Timeout):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return nil, fmt.Errorf("command execution timeout after %v", sc.Timeout)
	}
}

// ExecuteWithValidation 执行前验证参数格式
func ExecuteWithValidation(command string, args []string, validators map[string]*regexp.Regexp) ([]byte, error) {
	safeCmd := NewSafeCommand(command, args...)

	// 应用自定义验证规则
	for i, arg := range args {
		if i < len(validators) {
			validator := validators[fmt.Sprintf("arg%d", i)]
			if validator != nil && !validator.MatchString(arg) {
				return nil, fmt.Errorf("argument %d does not match required pattern", i)
			}
		}
	}

	return safeCmd.Execute()
}

// CheckServiceStatus 安全检查服务状态
func CheckServiceStatus(serviceName string) (bool, error) {
	// 验证服务名格式
	serviceRegex := regexp.MustCompile(`^[a-zA-Z0-9\-_\.]+$`)
	if !serviceRegex.MatchString(serviceName) {
		return false, fmt.Errorf("invalid service name format")
	}

	safeCmd := NewSafeCommand("systemctl", "is-active", serviceName)
	output, err := safeCmd.Execute()
	if err != nil {
		return false, err
	}

	return strings.TrimSpace(string(output)) == "active", nil
}

// GetProcessList 安全获取进程列表
func GetProcessList(processName string) ([]byte, error) {
	// 验证进程名格式
	processRegex := regexp.MustCompile(`^[a-zA-Z0-9\-_\.]+$`)
	if !processRegex.MatchString(processName) {
		return nil, fmt.Errorf("invalid process name format")
	}

	safeCmd := NewSafeCommand("ps", "-ef")
	output, err := safeCmd.Execute()
	if err != nil {
		return nil, err
	}

	// 在代码中过滤，而不是使用shell管道
	lines := strings.Split(string(output), "\n")
	var filtered []string

	for _, line := range lines {
		if strings.Contains(line, processName) && !strings.Contains(line, "grep") {
			filtered = append(filtered, line)
		}
	}

	return []byte(strings.Join(filtered, "\n")), nil
}

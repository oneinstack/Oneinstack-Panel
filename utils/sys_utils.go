package utils

import (
	"os/exec"
	"strings"
)

// checkFirewall 判断使用的防火墙
func CheckFirewall() string {
	if CheckServiceStatus("firewalld") {
		return "firewalld"
	}
	if CheckServiceStatus("ufw") {
		return "ufw"
	}
	if CheckServiceStatus("iptables") {
		return "iptables"
	}
	return "unknown"
}

// checkCommand 判断命令是否存在
func CheckCommand(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

// checkServiceStatus 检查 systemctl 是否存在并查询服务状态
func CheckServiceStatus(service string) bool {
	if !CheckCommand("systemctl") {
		return false
	}
	cmd := exec.Command("systemctl", "is-active", service)
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "active"
}

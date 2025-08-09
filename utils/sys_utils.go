package utils

import (
	"os/exec"
	"regexp"
)

// checkFirewall 判断使用的防火墙
func CheckFirewall() string {
	if status, _ := CheckServiceStatus("firewalld"); status {
		return "firewalld"
	}
	if status, _ := CheckServiceStatus("ufw"); status {
		return "ufw"
	}
	if status, _ := CheckServiceStatus("iptables"); status {
		return "iptables"
	}
	return "unknown"
}

// checkCommand 判断命令是否存在
func CheckCommand(cmd string) bool {
	// 验证命令名格式，防止注入
	cmdRegex := regexp.MustCompile(`^[a-zA-Z0-9\-_\.]+$`)
	if !cmdRegex.MatchString(cmd) {
		return false
	}
	_, err := exec.LookPath(cmd)
	return err == nil
}

package utils

import (
	"bytes"
	"net"
	"os/exec"
	"strings"
)

func GetLinuxIP() (string, error) {
	cmd := exec.Command("sh", "-c", "ip -4 route get 8.8.8.8 | awk '{print $7}' | tr -d '\\n'")
	var out bytes.Buffer
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return "", err
	}

	ipStr := strings.TrimSpace(out.String())
	if ip := net.ParseIP(ipStr); ip != nil && !ip.IsLoopback() {
		return ipStr, nil
	}

	return "", nil
}

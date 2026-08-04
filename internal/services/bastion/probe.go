package bastion

import (
	"context"
	"fmt"
	"net"
	"time"

	"oneinstack/internal/models"

	"golang.org/x/crypto/ssh"
)

// ProbeResult 连接探测结果
type ProbeResult struct {
	Reachable bool   `json:"reachable"`
	OSInfo    string `json:"osInfo,omitempty"`
	Hostname  string `json:"hostname,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Probe 测试与远端服务器的连通性并收集基本信息
func Probe(ctx context.Context, server *models.BastionServer, password string) *ProbeResult {
	result := &ProbeResult{}

	addr := net.JoinHostPort(server.Host, fmt.Sprintf("%d", server.Port))

	// TCP 连通性检查
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		result.Error = fmt.Sprintf("TCP 端口不可达: %v", err)
		return result
	}
	conn.Close()

	// SSH 握手 + 系统信息采集
	config, err := buildSSHClientConfig(server, password, 10*time.Second)
	if err != nil {
		result.Error = fmt.Sprintf("SSH 配置无效: %v", err)
		return result
	}

	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		result.Error = fmt.Sprintf("SSH 连接失败: %v", err)
		return result
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		result.Error = fmt.Sprintf("SSH 会话创建失败: %v", err)
		return result
	}
	defer session.Close()

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	var output []byte
	var execErr error

	go func() {
		output, execErr = session.CombinedOutput("uname -a && hostname")
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		result.Error = "探测超时"
		return result
	}

	if execErr != nil {
		result.Error = fmt.Sprintf("远程命令执行失败: %v", execErr)
		return result
	}

	result.Reachable = true
	info := string(output)
	if len(info) > 254 {
		info = info[:254]
	}
	result.OSInfo = info

	return result
}
